package gitlfs

import (
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
)

const (
	lfsContentType  = "application/vnd.git-lfs+json"
	batchOpDownload = "download"
	lfsMaxRetries   = 4
	lfsWorkerCount  = 2
	lfsBackoffBase  = 500 * time.Millisecond
	lfsBackoffMax   = 5 * time.Second
	lfsResumeDir    = ".gomall-cli-lfs"
)

type HydrateOptions struct {
	RepoDir     string
	RepoURL     string
	Token       string
	UserAgent   string
	Insecure    bool
	HTTPTimeout time.Duration
	IdleTimeout time.Duration
	ProgressOut io.Writer
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

	respMap, err := requestBatch(ctx, client, batchURL, opts.UserAgent, token, reqObjects)
	if err != nil {
		return 0, err
	}

	progress := newProgressReporter(opts.ProgressOut, totalBytes)
	progress.start()
	defer progress.finish()

	tasks := make([]lfsDownloadTask, 0, len(grouped))
	var resumedBytes int64
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
		tasks = append(tasks, lfsDownloadTask{
			oid:    oid,
			size:   files[0].Size,
			action: action,
			label:  buildTaskLabel(repoDir, files),
			part:   partFilePath(resumeDir, oid),
		})
		resumedBytes += resumeOffset(partFilePath(resumeDir, oid), files[0].Size)
	}
	progress.add(resumedBytes)

	if opts.ProgressOut != nil {
		_, _ = fmt.Fprintf(
			opts.ProgressOut,
			"Git LFS 待补全文件: %d 个（唯一对象: %d，已续传: %s）\n",
			len(pointers),
			len(tasks),
			formatBytes(resumedBytes),
		)
	}

	if err := runConcurrentDownloads(ctx, client, tasks, token, opts.UserAgent, idleTimeout, progress, downloadCache); err != nil {
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
	out          io.Writer
	totalBytes   int64
	downloaded   atomic.Int64
	startTime    time.Time
	done         chan struct{}
	wg           sync.WaitGroup
	mu           sync.Mutex
	lastBytes    int64
	lastSampleAt time.Time
}

func newProgressReporter(out io.Writer, totalBytes int64) *progressReporter {
	return &progressReporter{
		out:        out,
		totalBytes: totalBytes,
		done:       make(chan struct{}),
	}
}

func (p *progressReporter) start() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.startTime = time.Now()
	p.lastSampleAt = p.startTime
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.print(false)
			case <-p.done:
				p.print(true)
				return
			}
		}
	}()
}

func (p *progressReporter) add(n int64) {
	if n == 0 {
		return
	}
	p.downloaded.Add(n)
}

func (p *progressReporter) finish() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	close(p.done)
	p.wg.Wait()
}

func (p *progressReporter) print(final bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	doneBytes := p.downloaded.Load()
	if doneBytes > p.totalBytes {
		doneBytes = p.totalBytes
	}

	deltaBytes := doneBytes - p.lastBytes
	deltaSec := now.Sub(p.lastSampleAt).Seconds()
	var speed float64
	if deltaSec > 0 {
		speed = float64(deltaBytes) / deltaSec
	}
	if speed <= 0 {
		elapsed := now.Sub(p.startTime).Seconds()
		if elapsed > 0 {
			speed = float64(doneBytes) / elapsed
		}
	}

	remainBytes := p.totalBytes - doneBytes
	eta := "-"
	if speed > 0 && remainBytes > 0 {
		eta = formatDuration(time.Duration(float64(remainBytes)/speed) * time.Second)
	}

	percent := float64(doneBytes) * 100 / float64(p.totalBytes)
	line := fmt.Sprintf(
		"\rLFS下载进度: %6.2f%%  %s/%s  速度 %s/s  剩余 %s  预计 %s",
		percent,
		formatBytes(doneBytes),
		formatBytes(p.totalBytes),
		formatBytes(int64(speed)),
		formatBytes(remainBytes),
		eta,
	)
	if final {
		line += "\n"
	}
	_, _ = fmt.Fprint(p.out, line)

	p.lastBytes = doneBytes
	p.lastSampleAt = now
}

func (p *progressReporter) logDownloading(label string) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.out, "\r正在下载: %s\n", label)
}

func (p *progressReporter) logRetry(label string, attempt int, wait time.Duration, err error) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(
		p.out,
		"下载重试(%d): %s, 原因: %s, 等待 %s\n",
		attempt,
		label,
		classifyRetryReason(err),
		formatDuration(wait),
	)
}

func runConcurrentDownloads(
	ctx context.Context,
	client *http.Client,
	tasks []lfsDownloadTask,
	token, userAgent string,
	idleTimeout time.Duration,
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
			if progress != nil {
				progress.logDownloading(task.label)
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
	label string,
	progress *progressReporter,
) (string, error) {
	var lastErr error
	backoff := lfsBackoffBase

	for attempt := 1; attempt <= lfsMaxRetries; attempt++ {
		tmpPath, err := downloadObject(ctx, client, action, wantOID, wantSize, partPath, token, userAgent, idleTimeout, progress)
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

func downloadObject(
	ctx context.Context,
	client *http.Client,
	action batchAction,
	wantOID string,
	wantSize int64,
	partPath string,
	token, userAgent string,
	idleTimeout time.Duration,
	progress *progressReporter,
) (string, error) {
	offset := resumeOffset(partPath, wantSize)

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
