package gomallapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"gomall-cli/internal/config"
)

type TokenProvider func(ctx context.Context) (string, error)

// Client is a thin HTTP client for gomall APIs.
type Client struct {
	baseURL       *url.URL
	httpClient    *http.Client
	userAgent     string
	tokenHeader   string
	tokenProvider TokenProvider
}

func NewClient(cfg config.Config, tokenProvider TokenProvider) (*Client, error) {
	baseURL, err := url.Parse(cfg.API.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse api-base-url: %w", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.API.Insecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			transport.TLSClientConfig.InsecureSkipVerify = true
		}
	}

	return &Client{
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: cfg.API.Timeout, Transport: transport},
		userAgent:     cfg.API.UserAgent,
		tokenHeader:   cfg.Auth.TokenHeader,
		tokenProvider: tokenProvider,
	}, nil
}

func (c *Client) Do(ctx context.Context, method, apiPath string, reqBody any, requireAuth bool) (Envelope, error) {
	if requireAuth {
		if c.tokenProvider == nil {
			return Envelope{}, fmt.Errorf("token provider not configured")
		}
		token, err := c.tokenProvider(ctx)
		if err != nil {
			return Envelope{}, fmt.Errorf("load token: %w", err)
		}
		return c.do(ctx, method, apiPath, reqBody, token)
	}
	return c.do(ctx, method, apiPath, reqBody, "")
}

// DoWithToken sends request with explicit auth token (without loading from provider).
func (c *Client) DoWithToken(ctx context.Context, method, apiPath string, reqBody any, token string) (Envelope, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Envelope{}, fmt.Errorf("token cannot be empty")
	}
	return c.do(ctx, method, apiPath, reqBody, token)
}

// DoMultipartForm sends multipart/form-data request with key-value form fields.
func (c *Client) DoMultipartForm(
	ctx context.Context,
	method, apiPath string,
	fields map[string]string,
	requireAuth bool,
) (Envelope, error) {
	var token string
	if requireAuth {
		if c.tokenProvider == nil {
			return Envelope{}, fmt.Errorf("token provider not configured")
		}
		t, err := c.tokenProvider(ctx)
		if err != nil {
			return Envelope{}, fmt.Errorf("load token: %w", err)
		}
		token = t
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return Envelope{}, fmt.Errorf("write multipart field %q: %w", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		return Envelope{}, fmt.Errorf("finalize multipart body: %w", err)
	}

	return c.doWithReader(
		ctx,
		method,
		apiPath,
		&body,
		writer.FormDataContentType(),
		token,
	)
}

func (c *Client) do(ctx context.Context, method, apiPath string, reqBody any, token string) (Envelope, error) {
	var bodyReader io.Reader
	var contentType string
	if strings.TrimSpace(apiPath) == "" {
		return Envelope{}, fmt.Errorf("api path cannot be empty")
	}

	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return Envelope{}, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
		contentType = "application/json"
	}

	return c.doWithReader(ctx, method, apiPath, bodyReader, contentType, token)
}

func (c *Client) doWithReader(
	ctx context.Context,
	method, apiPath string,
	bodyReader io.Reader,
	contentType string,
	token string,
) (Envelope, error) {
	if strings.TrimSpace(apiPath) == "" {
		return Envelope{}, fmt.Errorf("api path cannot be empty")
	}
	u, err := c.resolveURL(apiPath)
	if err != nil {
		return Envelope{}, err
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return Envelope{}, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", c.userAgent)
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if token != "" {
		req.Header.Set(c.tokenHeader, token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Envelope{}, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Envelope{}, fmt.Errorf("read response body: %w", err)
	}

	var env Envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode response envelope (status=%d): %w, body=%s", resp.StatusCode, err, string(respBody))
	}

	if resp.StatusCode >= 400 && env.Message == "" {
		env.Message = fmt.Sprintf("http status %d", resp.StatusCode)
	}

	return env, nil
}

func (c *Client) resolveURL(apiPath string) (string, error) {
	if strings.HasPrefix(apiPath, "http://") || strings.HasPrefix(apiPath, "https://") {
		if _, err := url.Parse(apiPath); err != nil {
			return "", fmt.Errorf("invalid absolute url %q: %w", apiPath, err)
		}
		return apiPath, nil
	}

	rel, err := url.Parse(apiPath)
	if err != nil {
		return "", fmt.Errorf("parse api path %q: %w", apiPath, err)
	}
	full := c.baseURL.ResolveReference(rel)
	return full.String(), nil
}
