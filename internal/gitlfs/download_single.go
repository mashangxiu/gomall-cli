package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

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
