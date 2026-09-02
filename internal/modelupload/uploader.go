package modelupload

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
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const (
	lfsContentType = "application/vnd.git-lfs+json"
	uploadRetries  = 5
)

type Options struct {
	Dir                  string
	RepoURL              string
	Token                string
	UserAgent            string
	Insecure             bool
	Timeout              time.Duration
	LargeFileThresholdMB int
	Branch               string
	Username             string
	TransferURLOverride  string
	MergeRemote          bool
	Debug                bool
	DebugOut             io.Writer
	ProgressOut          io.Writer
}

type Result struct {
	Branch         string
	Commit         string
	Files          int
	LFSFiles       int
	UploadedLFS    int
	SkippedLFS     int
	TotalLFSBytes  int64
	LargeThreshold int64
}

type fileEntry struct {
	rel  string
	abs  string
	mode fs.FileMode
	size int64
}

type indexEntry struct {
	mode filemode.FileMode
	hash plumbing.Hash
	path string
}

type treeNode struct {
	files map[string]indexEntry
	dirs  map[string]*treeNode
}

type lfsObject struct {
	entry fileEntry
	oid   string
	size  int64
}

type batchRequest struct {
	Operation string               `json:"operation"`
	Transfers []string             `json:"transfers,omitempty"`
	Objects   []batchRequestObject `json:"objects"`
	Ref       *batchRef            `json:"ref,omitempty"`
	HashAlgo  string               `json:"hash_algo,omitempty"`
}

type batchRequestObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchRef struct {
	Name string `json:"name"`
}

type batchResponse struct {
	Objects []batchResponseObject `json:"objects"`
}

type batchResponseObject struct {
	OID     string                 `json:"oid"`
	Size    int64                  `json:"size"`
	Actions map[string]batchAction `json:"actions"`
	Error   *batchObjectError      `json:"error"`
}

type batchObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type batchAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

func Upload(ctx context.Context, opts Options) (Result, error) {
	opts.Dir = strings.TrimSpace(opts.Dir)
	opts.RepoURL = strings.TrimSpace(opts.RepoURL)
	opts.Token = strings.TrimSpace(opts.Token)
	if opts.Dir == "" {
		return Result{}, fmt.Errorf("upload dir cannot be empty")
	}
	if opts.RepoURL == "" {
		return Result{}, fmt.Errorf("repo url cannot be empty")
	}
	if opts.Token == "" {
		return Result{}, fmt.Errorf("gitlab token cannot be empty")
	}
	if opts.Branch == "" {
		opts.Branch = "master"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}
	threshold := int64(opts.LargeFileThresholdMB) * 1024 * 1024
	if threshold <= 0 {
		threshold = 10 * 1024 * 1024
	}

	root, err := resolveUploadRoot(opts.Dir)
	if err != nil {
		return Result{}, err
	}

	files, err := scanFiles(root)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("upload dir has no files")
	}
	lfsPlanFiles, lfsPlanBytes := countLFSCandidates(files, threshold)
	progress := newUploadProgressReporter(opts.ProgressOut, lfsPlanBytes)
	if lfsPlanFiles > 0 {
		printProgress(opts.ProgressOut, "Git LFS 待上传文件: %d 个（总大小: %s）", lfsPlanFiles, formatBytes(lfsPlanBytes))
	}
	debugf(opts, "upload root=%s repo=%s branch=%s files=%d threshold=%s merge_remote=%t", root, opts.RepoURL, opts.Branch, len(files), formatBytes(threshold), opts.MergeRemote)

	repo, err := ensureGitRepo(root, opts.RepoURL)
	if err != nil {
		return Result{}, err
	}
	var remoteEntries []indexEntry
	var parents []plumbing.Hash
	if opts.MergeRemote {
		debugf(opts, "fetch remote branch start: %s", opts.Branch)
		remoteEntries, parents, err = fetchRemoteBase(ctx, repo, opts)
		if err != nil {
			return Result{}, err
		}
		if len(parents) > 0 {
			debugf(opts, "fetch remote branch success: parent=%s remote_files=%d", parents[0], len(remoteEntries))
		} else {
			debugf(opts, "fetch remote branch empty: no remote parent found")
		}
	}

	client := newHTTPClient(opts.Timeout, opts.Insecure)
	var entries []indexEntry
	var lfsObjects []lfsObject
	var totalLFSBytes int64
	var attrSource string
	if lfsPlanFiles > 0 {
		progress.start()
		defer progress.finish()
	}

	for _, f := range files {
		if f.rel == ".gitattributes" {
			raw, readErr := os.ReadFile(f.abs)
			if readErr != nil {
				return Result{}, fmt.Errorf("read .gitattributes: %w", readErr)
			}
			attrSource = string(raw)
			continue
		}

		mode, blob, isLFS, obj, err := buildBlob(ctx, repo, f, threshold, client, opts, progress)
		if err != nil {
			return Result{}, err
		}
		entries = append(entries, indexEntry{mode: mode, hash: blob, path: f.rel})
		if isLFS {
			lfsObjects = append(lfsObjects, obj)
			totalLFSBytes += obj.size
			debugf(opts, "file classified as lfs: path=%s size=%s oid=%s", f.rel, formatBytes(f.size), obj.oid)
		} else {
			debugf(opts, "file classified as git blob: path=%s size=%s", f.rel, formatBytes(f.size))
		}
	}

	if len(lfsObjects) > 0 {
		attrBlob, err := writeBlob(repo, strings.NewReader(buildGitAttributes(attrSource, lfsObjects)))
		if err != nil {
			return Result{}, err
		}
		entries = append(entries, indexEntry{mode: filemode.Regular, hash: attrBlob, path: ".gitattributes"})
	} else if attrSource != "" {
		attrBlob, err := writeBlob(repo, strings.NewReader(attrSource))
		if err != nil {
			return Result{}, err
		}
		entries = append(entries, indexEntry{mode: filemode.Regular, hash: attrBlob, path: ".gitattributes"})
	}

	if len(remoteEntries) > 0 {
		entries = mergeIndexEntries(remoteEntries, entries)
		debugf(opts, "merged remote tree: final_files=%d local_files=%d remote_files=%d", len(entries), len(files), len(remoteEntries))
	} else {
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	}
	treeHash, err := writeTree(repo, entries)
	if err != nil {
		return Result{}, err
	}
	commitHash, err := commitTree(repo, treeHash, opts, parents)
	if err != nil {
		return Result{}, err
	}
	if err := setBranch(repo, opts.Branch, commitHash); err != nil {
		return Result{}, err
	}
	debugf(opts, "commit created: %s parents=%d tree=%s", commitHash, len(parents), treeHash)
	debugf(opts, "push branch start: %s", opts.Branch)
	if err := pushBranch(ctx, repo, opts.Branch, opts.Token, opts.Insecure, opts.ProgressOut); err != nil {
		return Result{}, err
	}
	debugf(opts, "push branch success: %s", opts.Branch)

	return Result{
		Branch:         opts.Branch,
		Commit:         commitHash.String(),
		Files:          len(entries),
		LFSFiles:       len(lfsObjects),
		UploadedLFS:    len(lfsObjects),
		TotalLFSBytes:  totalLFSBytes,
		LargeThreshold: threshold,
	}, nil
}

func resolveUploadRoot(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve upload dir: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve upload dir symlinks: %w", err)
	}
	stat, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat upload dir: %w", err)
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("upload path is not a directory: %s", root)
	}
	return root, nil
}

func scanFiles(root string) ([]fileEntry, error) {
	var files []fileEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".gomall-cli-lfs" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeType != 0 && info.Mode()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, "\n") {
			return fmt.Errorf("file path contains newline, unsupported by upload: %s", rel)
		}
		files = append(files, fileEntry{
			rel:  rel,
			abs:  path,
			mode: info.Mode(),
			size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan upload dir: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

func countLFSCandidates(files []fileEntry, threshold int64) (int, int64) {
	var count int
	var total int64
	for _, f := range files {
		if f.rel == ".gitattributes" {
			continue
		}
		if f.mode&fs.ModeSymlink != 0 {
			continue
		}
		if f.size >= threshold {
			count++
			total += f.size
		}
	}
	return count, total
}

func buildBlob(
	ctx context.Context,
	repo *gogit.Repository,
	f fileEntry,
	threshold int64,
	client *http.Client,
	opts Options,
	progress *uploadProgressReporter,
) (mode filemode.FileMode, blob plumbing.Hash, isLFS bool, obj lfsObject, err error) {
	if f.mode&fs.ModeSymlink != 0 {
		target, err := os.Readlink(f.abs)
		if err != nil {
			return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, fmt.Errorf("read symlink %s: %w", f.rel, err)
		}
		blob, err := writeBlob(repo, strings.NewReader(target))
		return filemode.Symlink, blob, false, lfsObject{}, err
	}

	if f.size >= threshold {
		oid, err := fileSHA256(f.abs)
		if err != nil {
			return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, fmt.Errorf("hash lfs file %s: %w", f.rel, err)
		}
		obj := lfsObject{entry: f, oid: oid, size: f.size}
		if err := uploadLFSObject(ctx, client, opts, obj, progress); err != nil {
			return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, err
		}
		pointer := lfsPointer(oid, f.size)
		blob, err := writeBlob(repo, strings.NewReader(pointer))
		if err != nil {
			return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, err
		}
		return fileMode(f.mode), blob, true, obj, nil
	}

	src, err := os.Open(f.abs)
	if err != nil {
		return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, fmt.Errorf("open file %s: %w", f.rel, err)
	}
	defer src.Close()
	blob, err = writeBlob(repo, src)
	if err != nil {
		return filemode.Empty, plumbing.ZeroHash, false, lfsObject{}, err
	}
	return fileMode(f.mode), blob, false, lfsObject{}, nil
}

func ensureGitRepo(root, repoURL string) (*gogit.Repository, error) {
	repo, err := gogit.PlainOpen(root)
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(root, ".git")); statErr == nil {
			return nil, fmt.Errorf("open git repo: %w", err)
		}
		repo, err = gogit.PlainInit(root, false)
		if err != nil {
			return nil, fmt.Errorf("init git repo: %w", err)
		}
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, fmt.Errorf("read git config: %w", err)
	}
	cfg.Remotes[gogit.DefaultRemoteName] = &gogitconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{repoURL},
	}
	if err := repo.SetConfig(cfg); err != nil {
		return nil, fmt.Errorf("write git config: %w", err)
	}
	return repo, nil
}

func fetchRemoteBase(ctx context.Context, repo *gogit.Repository, opts Options) ([]indexEntry, []plumbing.Hash, error) {
	branch := strings.TrimSpace(opts.Branch)
	if branch == "" {
		branch = "master"
	}
	refName := plumbing.NewBranchReferenceName(branch)
	remoteRefName := plumbing.NewRemoteReferenceName(gogit.DefaultRemoteName, branch)
	err := repo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: gogit.DefaultRemoteName,
		RefSpecs: []gogitconfig.RefSpec{
			gogitconfig.RefSpec("+" + string(refName) + ":" + string(remoteRefName)),
		},
		Auth: &githttp.BasicAuth{
			Username: "oauth2",
			Password: opts.Token,
		},
		InsecureSkipTLS: opts.Insecure,
		Progress:        opts.ProgressOut,
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		if errors.Is(err, plumbing.ErrReferenceNotFound) || isRemoteBranchMissingError(err) {
			debugf(opts, "fetch remote branch missing: %s", branch)
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("fetch remote branch %s: %w", branch, err)
	}

	remoteRef, err := repo.Reference(remoteRefName, true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			debugf(opts, "remote branch reference missing: %s", branch)
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read remote branch %s: %w", branch, err)
	}
	commit, err := repo.CommitObject(remoteRef.Hash())
	if err != nil {
		return nil, nil, fmt.Errorf("read remote commit %s: %w", remoteRef.Hash(), err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("read remote tree %s: %w", commit.Hash, err)
	}
	entries, err := flattenTree(tree)
	if err != nil {
		return nil, nil, err
	}
	return entries, []plumbing.Hash{commit.Hash}, nil
}

func isRemoteBranchMissingError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "couldn't find remote ref") ||
		strings.Contains(msg, "could not find remote ref") ||
		strings.Contains(msg, "reference not found") ||
		strings.Contains(msg, "couldn't find reference")
}

func flattenTree(tree *object.Tree) ([]indexEntry, error) {
	var entries []indexEntry
	err := tree.Files().ForEach(func(f *object.File) error {
		entries = append(entries, indexEntry{
			mode: f.Mode,
			hash: f.Hash,
			path: filepath.ToSlash(f.Name),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("flatten remote tree: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
}

func mergeIndexEntries(base, overlay []indexEntry) []indexEntry {
	merged := make(map[string]indexEntry, len(base)+len(overlay))
	for _, entry := range base {
		merged[entry.path] = entry
	}
	for _, entry := range overlay {
		merged[entry.path] = entry
	}
	out := make([]indexEntry, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func commitTree(repo *gogit.Repository, tree plumbing.Hash, opts Options, parents []plumbing.Hash) (plumbing.Hash, error) {
	username := strings.TrimSpace(opts.Username)
	if username == "" {
		username = "gomall-cli"
	}
	now := time.Now()
	commit := &object.Commit{
		Author: object.Signature{
			Name:  username,
			Email: username + "@gomall-cli.local",
			When:  now,
		},
		Committer: object.Signature{
			Name:  username,
			Email: username + "@gomall-cli.local",
			When:  now,
		},
		Message:      "Upload model\n",
		TreeHash:     tree,
		ParentHashes: parents,
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(obj)
}

func setBranch(repo *gogit.Repository, branch string, commit plumbing.Hash) error {
	refName := plumbing.NewBranchReferenceName(branch)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, commit)); err != nil {
		return fmt.Errorf("set branch ref: %w", err)
	}
	return repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, refName))
}

func pushBranch(ctx context.Context, repo *gogit.Repository, branch, token string, insecure bool, progress io.Writer) error {
	refName := plumbing.NewBranchReferenceName(branch)
	return repo.PushContext(ctx, &gogit.PushOptions{
		RemoteName: gogit.DefaultRemoteName,
		RefSpecs: []gogitconfig.RefSpec{
			gogitconfig.RefSpec(string(refName) + ":" + string(refName)),
		},
		Auth: &githttp.BasicAuth{
			Username: "oauth2",
			Password: token,
		},
		InsecureSkipTLS: insecure,
		Progress:        progress,
	})
}

func uploadLFSObject(ctx context.Context, client *http.Client, opts Options, obj lfsObject, progress *uploadProgressReporter) error {
	respObj, err := requestUploadAction(ctx, client, opts, obj)
	if err != nil {
		return err
	}
	if respObj.Error != nil {
		return fmt.Errorf("lfs upload rejected %s: code=%d message=%s", obj.entry.rel, respObj.Error.Code, respObj.Error.Message)
	}
	upload, ok := respObj.Actions["upload"]
	if !ok || strings.TrimSpace(upload.Href) == "" {
		if progress != nil {
			progress.add(obj.size)
			progress.logSkipped(obj.entry.rel)
		} else {
			printProgress(opts.ProgressOut, "LFS已存在: %s", obj.entry.rel)
		}
		return nil
	}
	if progress != nil {
		progress.logUploading(obj.entry.rel)
	}
	if err := uploadLFSContentWithRetry(ctx, client, opts, obj, upload, progress); err != nil {
		return err
	}
	if verify, ok := respObj.Actions["verify"]; ok && strings.TrimSpace(verify.Href) != "" {
		if err := verifyLFSContent(ctx, client, opts, obj, verify); err != nil {
			return err
		}
	}
	if progress != nil {
		progress.logComplete(obj.entry.rel, obj.size)
	} else {
		printProgress(opts.ProgressOut, "LFS上传完成: %s (%s)", obj.entry.rel, formatBytes(obj.size))
	}
	return nil
}

func requestUploadAction(ctx context.Context, client *http.Client, opts Options, obj lfsObject) (batchResponseObject, error) {
	batchURL, err := buildBatchURL(opts.RepoURL)
	if err != nil {
		return batchResponseObject{}, err
	}
	body := batchRequest{
		Operation: "upload",
		Transfers: []string{"ssh", "lfs-standalone-file", "basic"},
		Objects: []batchRequestObject{{
			OID:  obj.oid,
			Size: obj.size,
		}},
		Ref:      &batchRef{Name: "refs/heads/" + opts.Branch},
		HashAlgo: "sha256",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return batchResponseObject{}, err
	}
	debugf(opts, "lfs batch request: file=%s url=%s", obj.entry.rel, batchURL)
	debugJSON(opts, "lfs batch request body", raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, bytes.NewReader(raw))
	if err != nil {
		return batchResponseObject{}, err
	}
	req.Header.Set("Accept", lfsContentType)
	req.Header.Set("Content-Type", lfsContentType)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2:"+opts.Token)))
	if strings.TrimSpace(opts.UserAgent) != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		return batchResponseObject{}, fmt.Errorf("lfs upload batch request failed for %s: %w", obj.entry.rel, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return batchResponseObject{}, fmt.Errorf("read lfs upload batch response: %w", err)
	}
	debugf(opts, "lfs batch response: file=%s status=%d", obj.entry.rel, resp.StatusCode)
	debugJSON(opts, "lfs batch response body", respBody)
	if resp.StatusCode >= 400 {
		return batchResponseObject{}, fmt.Errorf("lfs upload batch status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var batchResp batchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return batchResponseObject{}, fmt.Errorf("decode lfs upload batch response: %w", err)
	}
	if len(batchResp.Objects) == 0 {
		return batchResponseObject{}, fmt.Errorf("lfs upload batch response missing object for %s", obj.entry.rel)
	}
	return batchResp.Objects[0], nil
}

func uploadLFSContentWithRetry(ctx context.Context, client *http.Client, opts Options, obj lfsObject, action batchAction, progress *uploadProgressReporter) error {
	var lastErr error
	for attempt := 1; attempt <= uploadRetries; attempt++ {
		sent, err := uploadLFSContent(ctx, client, opts, obj, action, progress)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if sent > 0 && progress != nil {
				progress.add(-sent)
			}
			lastErr = err
			if attempt == uploadRetries {
				break
			}
			wait := time.Duration(1<<uint(attempt-1)) * time.Second
			if progress != nil {
				progress.logRetry(obj.entry.rel, attempt+1, uploadRetries, wait, err)
			} else {
				printProgress(opts.ProgressOut, "LFS上传重试(%d/%d): %s，等待 %s，原因: %v", attempt+1, uploadRetries, obj.entry.rel, wait, err)
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		return nil
	}
	return lastErr
}

func uploadLFSContent(ctx context.Context, client *http.Client, opts Options, obj lfsObject, action batchAction, progress *uploadProgressReporter) (int64, error) {
	f, err := os.Open(obj.entry.abs)
	if err != nil {
		return 0, fmt.Errorf("open lfs file %s: %w", obj.entry.rel, err)
	}
	defer f.Close()

	uploadURL := strings.TrimSpace(action.Href)
	if strings.TrimSpace(opts.TransferURLOverride) != "" {
		rewritten, rewriteErr := rewriteTransferURL(uploadURL, opts.TransferURLOverride)
		if rewriteErr != nil {
			return 0, rewriteErr
		}
		if rewritten != uploadURL {
			debugf(opts, "lfs upload url overridden: file=%s before=%s after=%s", obj.entry.rel, uploadURL, rewritten)
		}
		uploadURL = rewritten
	}
	debugf(opts, "lfs upload request: file=%s method=PUT url=%s chunked=%t size=%s", obj.entry.rel, uploadURL, shouldUseChunked(action.Header), formatBytes(obj.size))
	var sent int64
	body := io.Reader(f)
	if progress != nil {
		body = &progressReader{
			r: f,
			onRead: func(n int64) {
				sent += n
				progress.add(n)
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return sent, fmt.Errorf("build lfs upload request: %w", err)
	}
	applyActionHeaders(req, action, opts)
	req.Header.Set("Content-Type", "application/octet-stream")
	if shouldUseChunked(action.Header) {
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	} else {
		req.ContentLength = obj.size
	}

	resp, err := client.Do(req)
	if err != nil {
		return sent, fmt.Errorf("upload lfs object %s: %w", obj.entry.rel, err)
	}
	defer resp.Body.Close()
	debugf(opts, "lfs upload response: file=%s status=%d", obj.entry.rel, resp.StatusCode)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		debugBody(opts, "lfs upload error response body", body)
		return sent, fmt.Errorf("upload lfs object %s status=%d body=%s", obj.entry.rel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return sent, nil
}

func verifyLFSContent(ctx context.Context, client *http.Client, opts Options, obj lfsObject, action batchAction) error {
	body := map[string]any{"oid": obj.oid, "size": obj.size}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action.Href, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	debugf(opts, "lfs verify request: file=%s url=%s", obj.entry.rel, action.Href)
	debugJSON(opts, "lfs verify request body", raw)
	applyActionHeaders(req, action, opts)
	req.Header.Set("Accept", lfsContentType)
	req.Header.Set("Content-Type", lfsContentType)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("verify lfs object %s: %w", obj.entry.rel, err)
	}
	defer resp.Body.Close()
	debugf(opts, "lfs verify response: file=%s status=%d", obj.entry.rel, resp.StatusCode)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		debugBody(opts, "lfs verify error response body", body)
		return fmt.Errorf("verify lfs object %s status=%d body=%s", obj.entry.rel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func applyActionHeaders(req *http.Request, action batchAction, opts Options) {
	if strings.TrimSpace(opts.UserAgent) != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}
	for k, v := range action.Header {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		req.Header.Set(key, v)
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2:"+opts.Token)))
	}
}

func shouldUseChunked(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(strings.TrimSpace(k), "Transfer-Encoding") &&
			strings.EqualFold(strings.TrimSpace(v), "chunked") {
			return true
		}
	}
	return false
}

func rewriteTransferURL(rawTransferURL, overrideBase string) (string, error) {
	transferURL, err := url.Parse(strings.TrimSpace(rawTransferURL))
	if err != nil {
		return "", fmt.Errorf("parse original lfs transfer url: %w", err)
	}
	if transferURL.Scheme == "" || transferURL.Host == "" {
		return "", fmt.Errorf("original lfs transfer url must include scheme and host: %q", rawTransferURL)
	}

	overrideURL, err := url.Parse(strings.TrimSpace(overrideBase))
	if err != nil {
		return "", fmt.Errorf("parse lfs transfer override url: %w", err)
	}
	if overrideURL.Scheme == "" || overrideURL.Host == "" {
		return "", fmt.Errorf("lfs transfer override url must include scheme and host: %q", overrideBase)
	}

	transferURL.Scheme = overrideURL.Scheme
	transferURL.Host = overrideURL.Host
	transferURL.User = overrideURL.User
	if strings.TrimSpace(overrideURL.Path) != "" && overrideURL.Path != "/" {
		transferURL.Path = "/" + strings.TrimPrefix(pathpkg.Join(overrideURL.Path, transferURL.Path), "/")
	}
	return transferURL.String(), nil
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

func buildGitAttributes(existing string, objects []lfsObject) string {
	var b strings.Builder
	existing = strings.TrimRight(existing, "\n")
	if existing != "" {
		b.WriteString(existing)
		b.WriteString("\n")
	}
	b.WriteString("\n# Generated by gomall-cli for Git LFS upload.\n")
	for _, obj := range objects {
		b.WriteString(quoteAttributePattern(obj.entry.rel))
		b.WriteString(" filter=lfs diff=lfs merge=lfs -text\n")
	}
	return b.String()
}

func quoteAttributePattern(rel string) string {
	return strconv.Quote("/" + strings.TrimPrefix(filepath.ToSlash(rel), "/"))
}

func lfsPointer(oid string, size int64) string {
	return fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, size)
}

func fileMode(mode fs.FileMode) filemode.FileMode {
	if mode&0o111 != 0 {
		return filemode.Executable
	}
	return filemode.Regular
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

func writeBlob(repo *gogit.Repository, r io.Reader) (plumbing.Hash, error) {
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(obj)
}

func writeTree(repo *gogit.Repository, entries []indexEntry) (plumbing.Hash, error) {
	root := &treeNode{
		files: map[string]indexEntry{},
		dirs:  map[string]*treeNode{},
	}
	for _, entry := range entries {
		parts := strings.Split(filepath.ToSlash(entry.path), "/")
		node := root
		for _, part := range parts[:len(parts)-1] {
			if part == "" {
				return plumbing.ZeroHash, fmt.Errorf("invalid git path: %q", entry.path)
			}
			if node.dirs[part] == nil {
				node.dirs[part] = &treeNode{
					files: map[string]indexEntry{},
					dirs:  map[string]*treeNode{},
				}
			}
			node = node.dirs[part]
		}
		name := parts[len(parts)-1]
		if name == "" {
			return plumbing.ZeroHash, fmt.Errorf("invalid git path: %q", entry.path)
		}
		node.files[name] = entry
	}
	return writeTreeNode(repo, root)
}

func writeTreeNode(repo *gogit.Repository, node *treeNode) (plumbing.Hash, error) {
	entries := make([]object.TreeEntry, 0, len(node.files)+len(node.dirs))
	for name, child := range node.dirs {
		hash, err := writeTreeNode(repo, child)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{
			Name: name,
			Mode: filemode.Dir,
			Hash: hash,
		})
	}
	for name, file := range node.files {
		entries = append(entries, object.TreeEntry{
			Name: name,
			Mode: file.mode,
			Hash: file.hash,
		})
	}
	sort.Sort(object.TreeEntrySorter(entries))

	tree := &object.Tree{Entries: entries}
	obj := repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(obj)
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
	return &http.Client{Timeout: timeout, Transport: transport}
}

func printProgress(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}

func debugf(opts Options, format string, args ...any) {
	if !opts.Debug {
		return
	}
	out := opts.DebugOut
	if out == nil {
		out = opts.ProgressOut
	}
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "[DEBUG] "+format+"\n", args...)
}

func debugJSON(opts Options, title string, raw []byte) {
	if !opts.Debug {
		return
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		debugf(opts, "%s: <empty>", title)
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, trimmed, "", "  "); err == nil {
		debugf(opts, "%s:\n%s", title, pretty.String())
		return
	}
	debugBody(opts, title, trimmed)
}

func debugBody(opts Options, title string, body []byte) {
	if !opts.Debug {
		return
	}
	const maxDebugBody = 64 * 1024
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > maxDebugBody {
		trimmed = append(trimmed[:maxDebugBody], []byte("\n...<truncated>")...)
	}
	debugf(opts, "%s:\n%s", title, string(trimmed))
}

func formatBytes(n int64) string {
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
