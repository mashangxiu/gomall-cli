package gitlfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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

type fileOffsetWriter struct {
	f      *os.File
	offset int64
}

func (w *fileOffsetWriter) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.offset)
	w.offset += int64(n)
	return n, err
}
