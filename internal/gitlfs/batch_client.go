package gitlfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
)

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
