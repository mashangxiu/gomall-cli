package gitlfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
