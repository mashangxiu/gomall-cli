package gitlfs

import (
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

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

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
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
