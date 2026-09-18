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
	logf := func(format string, args ...any) {
		if opts.ProgressOut == nil {
			return
		}
		_, _ = fmt.Fprintf(opts.ProgressOut, format+"\n", args...)
	}

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

	logf("Git LFS: 正在扫描 pointer 文件...")
	pointers, err := collectPointers(repoDir)
	if err != nil {
		return 0, err
	}
	allPointerCount := len(pointers)
	pointers, err = filterPointers(repoDir, pointers, opts.IncludePaths)
	if err != nil {
		return 0, err
	}
	if len(pointers) == 0 {
		if len(opts.IncludePaths) > 0 {
			logf("Git LFS: 未找到匹配指定文件的 pointer（扫描 %d 个 pointer）", allPointerCount)
		} else {
			logf("Git LFS: 未发现 pointer 文件")
		}
		return 0, nil
	}
	if len(opts.IncludePaths) > 0 {
		logf("Git LFS: pointer 扫描完成，匹配 %d/%d 个", len(pointers), allPointerCount)
	} else {
		logf("Git LFS: pointer 扫描完成，发现 %d 个", len(pointers))
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

	logf("Git LFS: 请求 Batch 下载链接（唯一对象: %d，总大小: %s）...", len(reqObjects), formatBytes(totalBytes))
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
	logf("Git LFS: Batch 下载链接获取完成")
	if err := applyDownloadURLOverride(respMap, opts.DownloadURLOverride, opts.DebugBatch, opts.DebugOut); err != nil {
		return 0, err
	}

	progress := newProgressReporter(opts.ProgressOut, totalBytes)

	tasks := make([]lfsDownloadTask, 0, len(grouped))
	var resumedBytes int64
	var localHitCount int
	logf("Git LFS: 正在检查本地断点状态...")
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
		label := buildTaskLabel(repoDir, files)
		partPath := partFilePath(resumeDir, oid)
		offset := resumeOffset(partPath, files[0].Size)
		if files[0].Size > 0 && offset == files[0].Size {
			if doneBytes, ok := multipartDoneBytes(partPath, files[0].Size); ok && doneBytes > 0 {
				logf("Git LFS: 发现分块断点: %s（已完成: %s / %s）", label, formatBytes(doneBytes), formatBytes(files[0].Size))
				offset = doneBytes
			} else {
				// Multipart downloads preallocate the part file to full size.
				// Only hash full-sized files when there is no multipart state.
				logf("Git LFS: 校验本地完整断点: %s（%s）", label, formatBytes(files[0].Size))
				gotOID, hashErr := fileSHA256WithProgress(partPath, label, files[0].Size, progress)
				if hashErr == nil && strings.EqualFold(gotOID, oid) {
					downloadCache[oid] = partPath
					resumedBytes += offset
					localHitCount++
					logf("Git LFS: 本地断点校验通过: %s", label)
					continue
				}
				logf("Git LFS: 本地断点校验失败，重新下载: %s", label)
				_ = os.Remove(partPath)
				offset = 0
			}
		}

		tasks = append(tasks, lfsDownloadTask{
			oid:    oid,
			size:   files[0].Size,
			action: action,
			label:  label,
			part:   partPath,
		})
		resumedBytes += offset
	}
	logf("Git LFS: 本地断点检查完成")
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

	logf("Git LFS: 开始下载任务（待下载对象: %d）", len(tasks))
	if err := runConcurrentDownloads(ctx, client, tasks, batchURL, token, opts.UserAgent, idleTimeout, chunkSize, opts.DownloadURLOverride, opts.DebugBatch, opts.DebugOut, progress, downloadCache); err != nil {
		return 0, err
	}

	logf("Git LFS: 正在替换 pointer 文件...")
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

	logf("Git LFS: pointer 文件替换完成")
	return hydrated, nil
}
