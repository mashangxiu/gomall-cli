package gitlfs

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

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
