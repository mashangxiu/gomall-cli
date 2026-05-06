package gitlfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
)

const (
	lfsContentType  = "application/vnd.git-lfs+json"
	batchOpDownload = "download"
	lfsMaxRetries   = 4
	lfsWorkerCount  = 2
	lfsBackoffBase  = 500 * time.Millisecond
	lfsBackoffMax   = 5 * time.Second
	lfsResumeDir    = ".gomall-cli-lfs"
	lfsChunkWorkers = 4
	lfsChunkSize    = 16 * 1024 * 1024  // 16MB
	lfsChunkMinSize = 256 * 1024 * 1024 // 256MB+
)

type HydrateOptions struct {
	RepoDir             string
	RepoURL             string
	Token               string
	UserAgent           string
	Insecure            bool
	HTTPTimeout         time.Duration
	IdleTimeout         time.Duration
	ChunkSize           int64
	DownloadURLOverride string
	ProgressOut         io.Writer
	DebugBatch          bool
	DebugOut            io.Writer
}

type pointerFile struct {
	Path string
	OID  string
	Size int64
	Mode fs.FileMode
}

type batchRequest struct {
	Operation string               `json:"operation"`
	Transfers []string             `json:"transfers,omitempty"`
	Objects   []batchRequestObject `json:"objects"`
}

type batchRequestObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchResponse struct {
	Transfer string                `json:"transfer"`
	Objects  []batchResponseObject `json:"objects"`
}

type batchResponseObject struct {
	OID     string                    `json:"oid"`
	Size    int64                     `json:"size"`
	Actions map[string]batchAction    `json:"actions"`
	Error   *batchResponseObjectError `json:"error"`
}

type batchResponseObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type batchAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type lfsDownloadTask struct {
	oid    string
	size   int64
	action batchAction
	label  string
	part   string
}

type rangeChunk struct {
	idx   int
	start int64
	end   int64
}

type multipartState struct {
	Size      int64  `json:"size"`
	ChunkSize int64  `json:"chunkSize"`
	Done      []bool `json:"done"`
}

func Hydrate(ctx context.Context, opts HydrateOptions) (int, error) {
	repoDir := strings.TrimSpace(opts.RepoDir)
	repoURL := strings.TrimSpace(opts.RepoURL)
	token := strings.TrimSpace(opts.Token)
	if repoDir == "" {
		return 0, fmt.Errorf("repo dir cannot be empty")
	}
	if repoURL == "" {
		return 0, fmt.Errorf("repo url cannot be empty")
	}
	if token == "" {
		return 0, fmt.Errorf("token cannot be empty")
	}

	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 2 * time.Minute
	}
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = lfsChunkSize
	}

	pointers, err := collectPointers(repoDir)
	if err != nil {
		return 0, err
	}
	if len(pointers) == 0 {
		return 0, nil
	}

	batchURL, err := buildBatchURL(repoURL)
	if err != nil {
		return 0, err
	}

	client := newHTTPClient(timeout, opts.Insecure)
	downloadCache := make(map[string]string) // oid -> resumable object file path
	resumeDir := filepath.Join(repoDir, lfsResumeDir)
	if err := os.MkdirAll(resumeDir, 0o755); err != nil {
		return 0, fmt.Errorf("create lfs resume dir: %w", err)
	}

	grouped := groupPointersByOID(pointers)
	oids := make([]string, 0, len(grouped))
	for oid := range grouped {
		oids = append(oids, oid)
	}

	reqObjects := make([]batchRequestObject, 0, len(oids))
	var totalBytes int64
	for _, oid := range oids {
		totalBytes += grouped[oid][0].Size
		reqObjects = append(reqObjects, batchRequestObject{
			OID:  oid,
			Size: grouped[oid][0].Size,
		})
	}

	respMap, err := requestBatch(
		ctx,
		client,
		batchURL,
		opts.UserAgent,
		token,
		reqObjects,
		opts.DebugBatch,
		opts.DebugOut,
	)
	if err != nil {
		return 0, err
	}
	if err := applyDownloadURLOverride(respMap, opts.DownloadURLOverride, opts.DebugBatch, opts.DebugOut); err != nil {
		return 0, err
	}

	progress := newProgressReporter(opts.ProgressOut, totalBytes)

	tasks := make([]lfsDownloadTask, 0, len(grouped))
	var resumedBytes int64
	var localHitCount int
	for oid, files := range grouped {
		obj, ok := respMap[oid]
		if !ok {
			return 0, fmt.Errorf("lfs batch response missing object: %s", oid)
		}
		if obj.Error != nil {
			return 0, fmt.Errorf("lfs batch object error oid=%s code=%d message=%s", oid, obj.Error.Code, obj.Error.Message)
		}
		action, ok := obj.Actions[batchOpDownload]
		if !ok || strings.TrimSpace(action.Href) == "" {
			return 0, fmt.Errorf("lfs download action missing for oid=%s", oid)
		}
		partPath := partFilePath(resumeDir, oid)
		// If local part is already full-sized, verify hash first so progress is accurate.
		offset := resumeOffset(partPath, files[0].Size)
		if files[0].Size > 0 && offset == files[0].Size {
			gotOID, hashErr := fileSHA256(partPath)
			if hashErr == nil && strings.EqualFold(gotOID, oid) {
				downloadCache[oid] = partPath
				resumedBytes += offset
				localHitCount++
				continue
			}
			if doneBytes, ok := multipartDoneBytes(partPath, files[0].Size); ok && doneBytes > 0 {
				offset = doneBytes
			} else {
				_ = os.Remove(partPath)
				_ = os.Remove(multipartStatePath(partPath))
				offset = 0
			}
		}

		tasks = append(tasks, lfsDownloadTask{
			oid:    oid,
			size:   files[0].Size,
			action: action,
			label:  buildTaskLabel(repoDir, files),
			part:   partPath,
		})
		resumedBytes += offset
	}
	progress.add(resumedBytes)

	if opts.ProgressOut != nil {
		_, _ = fmt.Fprintf(
			opts.ProgressOut,
			"Git LFS 待补全文件: %d 个（唯一对象: %d，本地命中: %d，待下载对象: %d，已续传: %s）\n",
			len(pointers),
			len(grouped),
			localHitCount,
			len(tasks),
			formatBytes(resumedBytes),
		)
	}
	progress.start()
	defer progress.finish()

	if err := runConcurrentDownloads(ctx, client, tasks, token, opts.UserAgent, idleTimeout, chunkSize, progress, downloadCache); err != nil {
		return 0, err
	}

	var hydrated int
	for oid, files := range grouped {
		tmpPath, ok := downloadCache[oid]
		if !ok {
			return hydrated, fmt.Errorf("missing downloaded lfs object: %s", oid)
		}
		for _, pf := range files {
			if err := replacePointerFile(tmpPath, pf.Path, pf.Mode); err != nil {
				return hydrated, err
			}
			hydrated++
		}
		_ = os.Remove(tmpPath)
	}

	return hydrated, nil
}

type progressReporter struct {
	out        io.Writer
	totalBytes int64
	bar        *progressbar.ProgressBar
	mu         sync.Mutex
	current    atomic.Int64
}

func newProgressReporter(out io.Writer, totalBytes int64) *progressReporter {
	return &progressReporter{
		out:        out,
		totalBytes: totalBytes,
	}
}

func (p *progressReporter) start() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}

	p.bar = progressbar.NewOptions64(
		p.totalBytes,
		progressbar.OptionSetWriter(p.out),
		progressbar.OptionSetDescription("LFS下载"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetWidth(36),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionThrottle(120*time.Millisecond),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerPadding: "░",
			SaucerHead:    "▓",
			BarStart:      "▕",
			BarEnd:        "▏",
		}),
		progressbar.OptionOnCompletion(func() {
			_, _ = fmt.Fprint(p.out, "\n")
		}),
	)
	_ = p.bar.Set64(p.current.Load())
}

func (p *progressReporter) add(n int64) {
	if n == 0 {
		return
	}
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	next := p.current.Load() + n
	if next < 0 {
		next = 0
	}
	if next > p.totalBytes {
		next = p.totalBytes
	}
	p.current.Store(next)
	if p.bar != nil {
		_ = p.bar.Set64(next)
	}
}

func (p *progressReporter) finish() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Finish()
	}
}

func (p *progressReporter) logDownloading(label string) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(p.out, "正在下载: %s\n", label)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}

func (p *progressReporter) logRetry(label string, attempt int, wait time.Duration, err error) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(
		p.out,
		"下载重试(%d): %s, 原因: %s, 等待 %s\n",
		attempt,
		label,
		classifyRetryReason(err),
		formatDuration(wait),
	)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}

func (p *progressReporter) logLocalHit(label string) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(p.out, "本地断点已完整: %s（跳过网络下载）\n", label)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}

func runConcurrentDownloads(
	ctx context.Context,
	client *http.Client,
	tasks []lfsDownloadTask,
	token, userAgent string,
	idleTimeout time.Duration,
	chunkSize int64,
	progress *progressReporter,
	downloadCache map[string]string,
) error {
	if len(tasks) == 0 {
		return nil
	}

	workerN := lfsWorkerCount
	if workerN > len(tasks) {
		workerN = len(tasks)
	}
	if workerN <= 0 {
		workerN = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	taskCh := make(chan lfsDownloadTask)
	var firstErr atomic.Value
	var cacheMu sync.Mutex
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for task := range taskCh {
			if err := ctx.Err(); err != nil {
				return
			}
			tmpPath, err := downloadObjectWithRetry(
				ctx,
				client,
				task.action,
				task.oid,
				task.size,
				task.part,
				token,
				userAgent,
				idleTimeout,
				chunkSize,
				task.label,
				progress,
			)
			if err != nil {
				if firstErr.Load() == nil {
					firstErr.Store(err)
					cancel()
				}
				return
			}
			cacheMu.Lock()
			downloadCache[task.oid] = tmpPath
			cacheMu.Unlock()
		}
	}

	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go worker()
	}

loopTasks:
	for _, task := range tasks {
		if firstErr.Load() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break loopTasks
		case taskCh <- task:
		}
	}
	close(taskCh)
	wg.Wait()

	if v := firstErr.Load(); v != nil {
		if err, ok := v.(error); ok {
			return err
		}
		return fmt.Errorf("lfs concurrent download failed")
	}
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func downloadObjectWithRetry(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	wantOID string,
	wantSize int64,
	partPath string,
	token, userAgent string,
	idleTimeout time.Duration,
	chunkSize int64,
	label string,
	progress *progressReporter,
) (string, error) {
	var lastErr error
	backoff := lfsBackoffBase

	for attempt := 1; attempt <= lfsMaxRetries; attempt++ {
		tmpPath, err := downloadObject(ctx, client, action, wantOID, wantSize, partPath, token, userAgent, idleTimeout, chunkSize, label, progress)
		if err == nil {
			return tmpPath, nil
		}
		lastErr = err
		if isHashMismatchError(err) {
			_ = os.Remove(partPath)
		}

		if ctx.Err() != nil {
			break
		}
		if attempt == lfsMaxRetries {
			break
		}

		wait := backoff
		if wait > lfsBackoffMax {
			wait = lfsBackoffMax
		}
		if progress != nil {
			progress.logRetry(label, attempt+1, wait, err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("download lfs object oid=%s canceled: %w", wantOID, ctx.Err())
		case <-timer.C:
		}
		backoff *= 2
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("download lfs object oid=%s canceled: %w", wantOID, ctxErr)
	}
	return "", fmt.Errorf("download lfs object oid=%s failed after %d attempts: %w", wantOID, lfsMaxRetries, lastErr)
}

func newHTTPClient(timeout time.Duration, insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			transport.TLSClientConfig.InsecureSkipVerify = true
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func collectPointers(root string) ([]pointerFile, error) {
	var out []pointerFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// LFS pointer files are tiny text files.
		if info.Size() <= 0 || info.Size() > 8*1024 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		oid, size, ok := parsePointer(raw)
		if !ok {
			return nil
		}
		out = append(out, pointerFile{
			Path: path,
			OID:  oid,
			Size: size,
			Mode: info.Mode(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan lfs pointers: %w", err)
	}
	return out, nil
}

func parsePointer(raw []byte) (string, int64, bool) {
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return "", 0, false
	}
	if strings.TrimSpace(lines[0]) != "version https://git-lfs.github.com/spec/v1" {
		return "", 0, false
	}

	oidLine := strings.TrimSpace(lines[1])
	sizeLine := strings.TrimSpace(lines[2])
	if !strings.HasPrefix(oidLine, "oid sha256:") || !strings.HasPrefix(sizeLine, "size ") {
		return "", 0, false
	}
	oid := strings.TrimSpace(strings.TrimPrefix(oidLine, "oid sha256:"))
	if len(oid) != 64 {
		return "", 0, false
	}
	if _, err := hex.DecodeString(oid); err != nil {
		return "", 0, false
	}
	sizeText := strings.TrimSpace(strings.TrimPrefix(sizeLine, "size "))
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return "", 0, false
	}
	return strings.ToLower(oid), size, true
}

func buildBatchURL(repoURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return "", fmt.Errorf("parse repo url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	u.Path = pathpkg.Join(strings.TrimSuffix(u.Path, "/"), "info/lfs/objects/batch")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func groupPointersByOID(pointers []pointerFile) map[string][]pointerFile {
	out := make(map[string][]pointerFile, len(pointers))
	for _, pf := range pointers {
		out[pf.OID] = append(out[pf.OID], pf)
	}
	return out
}

func buildTaskLabel(repoDir string, files []pointerFile) string {
	if len(files) == 0 {
		return "(unknown file)"
	}
	first := files[0].Path
	if rel, err := filepath.Rel(repoDir, first); err == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, "..") {
		first = rel
	} else {
		first = filepath.Base(first)
	}
	if len(files) == 1 {
		return first
	}
	return fmt.Sprintf("%s (+%d 个同OID文件)", first, len(files)-1)
}

func requestBatch(
	ctx context.Context,
	client *http.Client,
	batchURL, userAgent, token string,
	objects []batchRequestObject,
	debugBatch bool,
	debugOut io.Writer,
) (map[string]batchResponseObject, error) {
	body := batchRequest{
		Operation: batchOpDownload,
		Transfers: []string{"basic"},
		Objects:   objects,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal lfs batch request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, strings.NewReader(string(b)))
	if err != nil {
		return nil, fmt.Errorf("build lfs batch request: %w", err)
	}
	req.Header.Set("Accept", lfsContentType)
	req.Header.Set("Content-Type", lfsContentType)
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Authorization", "Basic "+basicAuth("oauth2", token))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lfs batch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read lfs batch response: %w", err)
	}
	if debugBatch {
		printBatchDebug(debugOut, resp.StatusCode, respBody)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("lfs batch http status %d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var batchResp batchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return nil, fmt.Errorf("decode lfs batch response: %w", err)
	}

	out := make(map[string]batchResponseObject, len(batchResp.Objects))
	for _, obj := range batchResp.Objects {
		out[obj.OID] = obj
	}
	return out, nil
}

func applyDownloadURLOverride(resp map[string]batchResponseObject, overrideBase string, debug bool, debugOut io.Writer) error {
	overrideBase = strings.TrimSpace(overrideBase)
	if overrideBase == "" {
		return nil
	}
	for oid, obj := range resp {
		action, ok := obj.Actions[batchOpDownload]
		if !ok || strings.TrimSpace(action.Href) == "" {
			continue
		}
		original := action.Href
		rewritten, err := rewriteDownloadURL(action.Href, overrideBase)
		if err != nil {
			return fmt.Errorf("rewrite lfs download url for oid=%s: %w", oid, err)
		}
		action.Href = rewritten
		obj.Actions[batchOpDownload] = action
		resp[oid] = obj
		if debug && debugOut != nil && rewritten != original {
			_, _ = fmt.Fprintf(debugOut, "[DEBUG] LFS download URL overridden oid=%s\n[DEBUG] after: %s\n", oid, rewritten)
		}
	}
	return nil
}

func rewriteDownloadURL(rawDownloadURL, overrideBase string) (string, error) {
	downloadURL, err := url.Parse(strings.TrimSpace(rawDownloadURL))
	if err != nil {
		return "", fmt.Errorf("parse original download url: %w", err)
	}
	if downloadURL.Scheme == "" || downloadURL.Host == "" {
		return "", fmt.Errorf("original download url must include scheme and host: %q", rawDownloadURL)
	}

	overrideURL, err := url.Parse(strings.TrimSpace(overrideBase))
	if err != nil {
		return "", fmt.Errorf("parse override url: %w", err)
	}
	if overrideURL.Scheme == "" || overrideURL.Host == "" {
		return "", fmt.Errorf("override url must include scheme and host: %q", overrideBase)
	}

	downloadURL.Scheme = overrideURL.Scheme
	downloadURL.Host = overrideURL.Host
	downloadURL.User = overrideURL.User
	if strings.TrimSpace(overrideURL.Path) != "" && overrideURL.Path != "/" {
		downloadURL.Path = "/" + strings.TrimPrefix(pathpkg.Join(overrideURL.Path, downloadURL.Path), "/")
	}
	return downloadURL.String(), nil
}

func printBatchDebug(out io.Writer, statusCode int, body []byte) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "[DEBUG] LFS Batch API status=%d\n", statusCode)
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		_, _ = fmt.Fprintln(out, "[DEBUG] LFS Batch API body=<empty>")
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, trimmed, "", "  "); err == nil {
		_, _ = fmt.Fprintf(out, "[DEBUG] LFS Batch API body:\n%s\n", pretty.String())
		return
	}
	_, _ = fmt.Fprintf(out, "[DEBUG] LFS Batch API body:\n%s\n", string(trimmed))
}

func downloadObject(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	wantOID string,
	wantSize int64,
	partPath string,
	token, userAgent string,
	idleTimeout time.Duration,
	chunkSize int64,
	label string,
	progress *progressReporter,
) (string, error) {
	offset := resumeOffset(partPath, wantSize)
	if wantSize > 0 && offset == wantSize {
		gotOID, err := fileSHA256(partPath)
		if err == nil && strings.EqualFold(gotOID, wantOID) {
			if progress != nil {
				progress.logLocalHit(label)
			}
			return partPath, nil
		}
		if doneBytes, ok := multipartDoneBytes(partPath, wantSize); ok {
			if progress != nil && offset > doneBytes {
				progress.add(-(offset - doneBytes))
			}
			offset = 0
		} else {
			if progress != nil && offset > 0 {
				progress.add(-offset)
			}
			_ = os.Remove(partPath)
			_ = os.Remove(multipartStatePath(partPath))
			offset = 0
		}
	}
	if offset == 0 && wantSize >= lfsChunkMinSize {
		if ok, _ := supportsRangeDownload(ctx, client, action, token, userAgent); ok {
			if progress != nil {
				progress.logDownloading(label + "（分块并发）")
			}
			return downloadObjectMultipart(ctx, client, action, wantOID, wantSize, partPath, token, userAgent, idleTimeout, chunkSize, progress)
		}
	}
	if progress != nil {
		progress.logDownloading(label)
	}

	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()
	var idleTriggered atomic.Bool
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())
	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)

	if idleTimeout > 0 {
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopWatchdog:
					return
				case <-reqCtx.Done():
					return
				case <-ticker.C:
					last := time.Unix(0, lastReadAt.Load())
					if time.Since(last) > idleTimeout {
						idleTriggered.Store(true)
						cancelReq()
						return
					}
				}
			}
		}()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, action.Href, nil)
	if err != nil {
		return "", fmt.Errorf("build lfs object download request: %w", err)
	}
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	for k, v := range action.Header {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Basic "+basicAuth("oauth2", token))
	}

	resp, err := client.Do(req)
	if err != nil {
		if idleTriggered.Load() {
			return "", fmt.Errorf("idle timeout: no data received for %s", idleTimeout)
		}
		return "", fmt.Errorf("download lfs object oid=%s: %w", wantOID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", fmt.Errorf("download lfs object oid=%s failed: http status %d body=%s", wantOID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return "", fmt.Errorf("create lfs part dir: %w", err)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		if resp.StatusCode == http.StatusOK {
			// Server ignored Range; restart from 0 and correct preloaded progress.
			if progress != nil {
				progress.add(-offset)
			}
			offset = 0
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
	} else {
		flags |= os.O_TRUNC
	}

	tmp, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("open lfs part file: %w", err)
	}

	n, copyErr := io.Copy(tmp, &progressReader{
		r: resp.Body,
		onRead: func(n int64) {
			lastReadAt.Store(time.Now().UnixNano())
			if progress != nil {
				progress.add(n)
			}
		},
	})
	closeErr := tmp.Close()
	if copyErr != nil {
		if idleTriggered.Load() {
			return "", fmt.Errorf("idle timeout: no data received for %s", idleTimeout)
		}
		return "", fmt.Errorf("write lfs object oid=%s: %w", wantOID, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close lfs temp file oid=%s: %w", wantOID, closeErr)
	}

	fullSize := offset + n
	if wantSize >= 0 && fullSize > wantSize {
		if progress != nil {
			progress.add(-(fullSize - wantSize))
		}
		_ = os.Remove(partPath)
		return "", fmt.Errorf("lfs object size overflow oid=%s want=%d got=%d", wantOID, wantSize, fullSize)
	}

	// Partial object remains on disk for byte-range resume in next retry/run.
	if fullSize < wantSize {
		return "", fmt.Errorf("partial lfs object oid=%s downloaded=%d/%d", wantOID, fullSize, wantSize)
	}
	if fullSize != wantSize {
		_ = os.Remove(partPath)
		return "", fmt.Errorf("lfs object size mismatch oid=%s want=%d got=%d", wantOID, wantSize, fullSize)
	}

	gotOID, hashErr := fileSHA256(partPath)
	if hashErr != nil {
		return "", fmt.Errorf("hash lfs object oid=%s: %w", wantOID, hashErr)
	}
	if !strings.EqualFold(gotOID, wantOID) {
		if progress != nil {
			progress.add(-fullSize)
		}
		_ = os.Remove(partPath)
		return "", fmt.Errorf("lfs object hash mismatch want=%s got=%s", wantOID, gotOID)
	}
	return partPath, nil
}

func supportsRangeDownload(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	token, userAgent string,
) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, action.Href, nil)
	if err != nil {
		return false, err
	}
	applyActionHeaders(req, action, token, userAgent)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	return resp.StatusCode == http.StatusPartialContent, nil
}

func downloadObjectMultipart(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	wantOID string,
	wantSize int64,
	partPath string,
	token, userAgent string,
	idleTimeout time.Duration,
	chunkSize int64,
	progress *progressReporter,
) (string, error) {
	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return "", fmt.Errorf("create lfs part dir: %w", err)
	}
	statePath := multipartStatePath(partPath)
	if chunkSize <= 0 {
		chunkSize = lfsChunkSize
	}
	chunks := buildChunks(wantSize, chunkSize)
	state, err := loadOrInitMultipartState(statePath, wantSize, chunkSize, len(chunks))
	if err != nil {
		return "", err
	}

	needResetFile := false
	if info, err := os.Stat(partPath); err == nil {
		if info.Size() != wantSize {
			needResetFile = true
		}
	} else if os.IsNotExist(err) {
		needResetFile = true
	} else {
		return "", fmt.Errorf("stat lfs part file: %w", err)
	}
	if len(state.Done) != len(chunks) {
		needResetFile = true
		state.Done = make([]bool, len(chunks))
	}

	flags := os.O_CREATE | os.O_RDWR
	if needResetFile {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("open lfs part file: %w", err)
	}
	if needResetFile {
		if err := f.Truncate(wantSize); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("truncate lfs part file: %w", err)
		}
		if err := saveMultipartState(statePath, state); err != nil {
			_ = f.Close()
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close lfs part file: %w", err)
	}

	// Reload state after potential reset write.
	state, err = loadOrInitMultipartState(statePath, wantSize, chunkSize, len(chunks))
	if err != nil {
		return "", err
	}
	if len(state.Done) != len(chunks) {
		state.Done = make([]bool, len(chunks))
		if err := saveMultipartState(statePath, state); err != nil {
			return "", err
		}
	}

	var alreadyDoneBytes int64
	for i := range chunks {
		if state.Done[i] {
			alreadyDoneBytes += chunks[i].end - chunks[i].start + 1
		}
	}
	if alreadyDoneBytes > wantSize {
		alreadyDoneBytes = 0
		state.Done = make([]bool, len(chunks))
		if err := saveMultipartState(statePath, state); err != nil {
			return "", err
		}
	}

	if progress != nil && alreadyDoneBytes > 0 {
		progress.add(alreadyDoneBytes)
	}

	if len(chunks) == 0 {
		return partPath, nil
	}

	if alreadyDoneBytes == wantSize {
		gotOID, hashErr := fileSHA256(partPath)
		if hashErr == nil && strings.EqualFold(gotOID, wantOID) {
			_ = os.Remove(statePath)
			return partPath, nil
		}
		state.Done = make([]bool, len(chunks))
		if err := saveMultipartState(statePath, state); err != nil {
			return "", err
		}
		if f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644); err == nil {
			_ = f.Truncate(wantSize)
			_ = f.Close()
		}
		if progress != nil && alreadyDoneBytes > 0 {
			progress.add(-alreadyDoneBytes)
		}
	}

	workerN := lfsChunkWorkers
	if workerN > len(chunks) {
		workerN = len(chunks)
	}
	if workerN <= 0 {
		workerN = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunkCh := make(chan rangeChunk)
	var firstErr atomic.Value
	var wg sync.WaitGroup
	var stateMu sync.Mutex

	worker := func() {
		defer wg.Done()
		for ch := range chunkCh {
			if ctx.Err() != nil {
				return
			}
			if err := downloadChunkWithRetry(ctx, client, action, token, userAgent, idleTimeout, partPath, ch, progress); err != nil {
				if firstErr.Load() == nil {
					firstErr.Store(err)
					cancel()
				}
				return
			}
			stateMu.Lock()
			state.Done[ch.idx] = true
			saveErr := saveMultipartState(statePath, state)
			stateMu.Unlock()
			if saveErr != nil {
				if firstErr.Load() == nil {
					firstErr.Store(saveErr)
					cancel()
				}
				return
			}
		}
	}
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go worker()
	}

loop:
	for _, ch := range chunks {
		if state.Done[ch.idx] {
			continue
		}
		if firstErr.Load() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break loop
		case chunkCh <- ch:
		}
	}
	close(chunkCh)
	wg.Wait()

	if v := firstErr.Load(); v != nil {
		if e, ok := v.(error); ok {
			return "", e
		}
		return "", fmt.Errorf("multipart lfs download failed")
	}
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return "", err
	}

	gotOID, hashErr := fileSHA256(partPath)
	if hashErr != nil {
		return "", fmt.Errorf("hash lfs object oid=%s: %w", wantOID, hashErr)
	}
	if !strings.EqualFold(gotOID, wantOID) {
		if progress != nil {
			progress.add(-wantSize)
		}
		_ = os.Remove(partPath)
		_ = os.Remove(statePath)
		return "", fmt.Errorf("lfs object hash mismatch want=%s got=%s", wantOID, gotOID)
	}
	_ = os.Remove(statePath)
	return partPath, nil
}

func downloadChunkWithRetry(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	token, userAgent string,
	idleTimeout time.Duration,
	partPath string,
	ch rangeChunk,
	progress *progressReporter,
) error {
	var lastErr error
	backoff := lfsBackoffBase
	for attempt := 1; attempt <= lfsMaxRetries; attempt++ {
		if err := downloadChunk(ctx, client, action, token, userAgent, idleTimeout, partPath, ch, progress); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil || attempt == lfsMaxRetries {
			break
		}
		wait := backoff
		if wait > lfsBackoffMax {
			wait = lfsBackoffMax
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return lastErr
}

func downloadChunk(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	token, userAgent string,
	idleTimeout time.Duration,
	partPath string,
	ch rangeChunk,
	progress *progressReporter,
) error {
	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()
	var idleTriggered atomic.Bool
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())
	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)

	if idleTimeout > 0 {
		go func() {
			t := time.NewTicker(1 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stopWatchdog:
					return
				case <-reqCtx.Done():
					return
				case <-t.C:
					last := time.Unix(0, lastReadAt.Load())
					if time.Since(last) > idleTimeout {
						idleTriggered.Store(true)
						cancelReq()
						return
					}
				}
			}
		}()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, action.Href, nil)
	if err != nil {
		return err
	}
	applyActionHeaders(req, action, token, userAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ch.start, ch.end))

	resp, err := client.Do(req)
	if err != nil {
		if idleTriggered.Load() {
			return fmt.Errorf("idle timeout: no data received for %s", idleTimeout)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("range download failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	f, err := os.OpenFile(partPath, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := &fileOffsetWriter{
		f:      f,
		offset: ch.start,
	}
	n, copyErr := io.Copy(writer, &progressReader{
		r: resp.Body,
		onRead: func(n int64) {
			lastReadAt.Store(time.Now().UnixNano())
			if progress != nil {
				progress.add(n)
			}
		},
	})
	if copyErr != nil {
		if progress != nil && n > 0 {
			progress.add(-n)
		}
		return copyErr
	}
	expected := ch.end - ch.start + 1
	if n != expected {
		if progress != nil {
			progress.add(-n)
		}
		return fmt.Errorf("range chunk size mismatch expected=%d got=%d", expected, n)
	}
	return nil
}

func buildChunks(total, chunkSize int64) []rangeChunk {
	if total <= 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = total
	}
	var out []rangeChunk
	for i, start := 0, int64(0); start < total; i, start = i+1, start+chunkSize {
		end := start + chunkSize - 1
		if end >= total {
			end = total - 1
		}
		out = append(out, rangeChunk{idx: i, start: start, end: end})
	}
	return out
}

func applyActionHeaders(req *http.Request, action batchAction, token, userAgent string) {
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	for k, v := range action.Header {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Basic "+basicAuth("oauth2", token))
	}
}

type fileOffsetWriter struct {
	f      *os.File
	offset int64
}

func (w *fileOffsetWriter) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.offset)
	w.offset += int64(n)
	return n, err
}

func replacePointerFile(src, dst string, mode fs.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open lfs temp object: %w", err)
	}
	defer srcFile.Close()

	tmpDst, err := os.CreateTemp(filepath.Dir(dst), ".gomall-lfs-*")
	if err != nil {
		return fmt.Errorf("create destination temp file: %w", err)
	}
	tmpDstPath := tmpDst.Name()

	if _, err := io.Copy(tmpDst, srcFile); err != nil {
		_ = tmpDst.Close()
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("write destination temp file: %w", err)
	}
	if err := tmpDst.Chmod(mode.Perm()); err != nil {
		_ = tmpDst.Close()
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("chmod destination temp file: %w", err)
	}
	if err := tmpDst.Close(); err != nil {
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("close destination temp file: %w", err)
	}
	if err := os.Rename(tmpDstPath, dst); err != nil {
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("replace pointer file %s: %w", dst, err)
	}
	return nil
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

func partFilePath(resumeDir, oid string) string {
	return filepath.Join(resumeDir, oid+".part")
}

func multipartStatePath(partPath string) string {
	return partPath + ".state.json"
}

func resumeOffset(partPath string, wantSize int64) int64 {
	info, err := os.Stat(partPath)
	if err != nil {
		return 0
	}
	size := info.Size()
	if size < 0 {
		return 0
	}
	if wantSize > 0 && size > wantSize {
		return 0
	}
	return size
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isHashMismatchError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "hash mismatch")
}

func loadOrInitMultipartState(path string, size, chunkSize int64, chunkCount int) (*multipartState, error) {
	st, err := loadMultipartState(path)
	if err == nil {
		if st.Size == size && st.ChunkSize == chunkSize && len(st.Done) == chunkCount {
			return st, nil
		}
	}
	st = &multipartState{
		Size:      size,
		ChunkSize: chunkSize,
		Done:      make([]bool, chunkCount),
	}
	if err := saveMultipartState(path, st); err != nil {
		return nil, err
	}
	return st, nil
}

func loadMultipartState(path string) (*multipartState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st multipartState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveMultipartState(path string, st *multipartState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal multipart state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write multipart state temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace multipart state file: %w", err)
	}
	return nil
}

func multipartDoneBytes(partPath string, wantSize int64) (int64, bool) {
	statePath := multipartStatePath(partPath)
	st, err := loadMultipartState(statePath)
	if err != nil {
		return 0, false
	}
	if st.Size != wantSize || st.ChunkSize <= 0 {
		return 0, false
	}
	chunks := buildChunks(wantSize, st.ChunkSize)
	if len(chunks) == 0 || len(st.Done) != len(chunks) {
		return 0, false
	}
	var doneBytes int64
	for i := 0; i < len(chunks); i++ {
		if st.Done[i] {
			doneBytes += chunks[i].end - chunks[i].start + 1
		}
	}
	if doneBytes <= 0 {
		return 0, true
	}
	if doneBytes > wantSize {
		doneBytes = wantSize
	}
	return doneBytes, true
}

type progressReader struct {
	r      io.Reader
	onRead func(int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 && pr.onRead != nil {
		pr.onRead(int64(n))
	}
	return n, err
}

func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	suffixes := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	return fmt.Sprintf("%.1f%s", value, suffixes[exp])
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	sec := int64(d.Round(time.Second) / time.Second)
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func classifyRetryReason(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "idle timeout"):
		return "idle timeout"
	case strings.Contains(msg, "http status 401"):
		return "http 401 unauthorized"
	case strings.Contains(msg, "http status 403"):
		return "http 403 forbidden"
	case strings.Contains(msg, "http status 404"):
		return "http 404 not found"
	case strings.Contains(msg, "connection reset by peer"):
		return "connection reset"
	case strings.Contains(msg, "broken pipe"):
		return "broken pipe"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return strings.TrimSpace(err.Error())
	}
}
