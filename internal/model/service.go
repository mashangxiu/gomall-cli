package model

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gomall-cli/internal/gomallapi"
)

const searchPath = "/goMallApi/api/v2/models"
const detailPathPrefix = "/goMallApi/api/v2/models/detail"

// Service handles model-related API operations.
type Service struct {
	client *gomallapi.Client
}

func NewService(client *gomallapi.Client) *Service {
	return &Service{client: client}
}

type SearchOptions struct {
	Name string
	Page int
	Size int
}

type CreatedOptions struct {
	Name string
	Page int
	Size int
}

type SearchResult struct {
	Items      []ModelItem `json:"data"`
	Total      int         `json:"total"`
	Page       int         `json:"page"`
	TotalPages int         `json:"total_pages"`
}

type ModelItem struct {
	ID          int64  `json:"id"`
	CreatedAt   int64  `json:"created_at"`
	Name        string `json:"name"`
	CNName      string `json:"cn_name"`
	Description string `json:"description"`
	Username    string `json:"username"`
	License     string `json:"license"`
	LikeCount   int    `json:"like_count"`
	StarCount   int    `json:"star_count"`
	ViewCount   int    `json:"view_count"`
	UseCount    int    `json:"use_count"`
	LabAddress  string `json:"lab_address"`
}

type ModelDetail struct {
	ID          int64  `json:"id"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	Name        string `json:"name"`
	CNName      string `json:"cn_name"`
	Description string `json:"description"`
	Username    string `json:"username"`
	Source      string `json:"source"`
	License     string `json:"license"`
	LabAddress  string `json:"lab_address"`
	Readme      string `json:"readme_content"`
}

func (s *Service) Search(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return SearchResult{}, fmt.Errorf("name cannot be empty")
	}

	env, err := s.client.Do(ctx, http.MethodGet, buildSearchPath(name, normalizePage(opts.Page), normalizeSize(opts.Size, 10)), nil, true)
	if err != nil {
		return SearchResult{}, err
	}
	return decodeSearchResult(env)
}

func (s *Service) Created(ctx context.Context, opts CreatedOptions) (SearchResult, error) {
	path := buildCreatedPath(strings.TrimSpace(opts.Name), normalizePage(opts.Page), normalizeSize(opts.Size, 16))
	env, err := s.client.Do(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return SearchResult{}, err
	}
	return decodeSearchResult(env)
}

func (s *Service) Detail(ctx context.Context, author, name string) (ModelDetail, error) {
	author = strings.TrimSpace(author)
	name = strings.TrimSpace(name)
	if author == "" || name == "" {
		return ModelDetail{}, fmt.Errorf("author and name cannot be empty")
	}

	path := buildDetailPath(author, name)
	env, err := s.client.Do(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return ModelDetail{}, err
	}
	if env.Code != 200 {
		return ModelDetail{}, fmt.Errorf("model detail failed: code=%d message=%s", env.Code, env.Message)
	}

	var detail ModelDetail
	if err := env.DecodeData(&detail); err != nil {
		return ModelDetail{}, err
	}
	return detail, nil
}

func buildSearchPath(name string, page, size int) string {
	q := url.Values{}
	q.Set("size", strconv.Itoa(size))
	q.Set("page", strconv.Itoa(page))
	q.Set("order_by", "comprehensive")
	q.Set("task_ids", "")
	q.Set("name", name)
	q.Set("search_type", "public")
	q.Set("sort", "desc")
	return searchPath + "?" + q.Encode()
}

func buildCreatedPath(name string, page, size int) string {
	q := url.Values{}
	q.Set("size", strconv.Itoa(size))
	q.Set("page", strconv.Itoa(page))
	q.Set("order_by", "updated_at")
	q.Set("task_ids", "")
	q.Set("name", name)
	q.Set("search_type", "create")
	q.Set("sort", "desc")
	return searchPath + "?" + q.Encode()
}

func buildDetailPath(author, name string) string {
	return detailPathPrefix + "/" + url.PathEscape(author) + "/" + url.PathEscape(name)
}

func decodeSearchResult(env gomallapi.Envelope) (SearchResult, error) {
	if env.Code != 200 {
		return SearchResult{}, fmt.Errorf("model search failed: code=%d message=%s", env.Code, env.Message)
	}
	var result SearchResult
	if err := env.DecodeData(&result); err != nil {
		return SearchResult{}, err
	}
	return result, nil
}

func normalizePage(page int) int {
	if page <= 0 {
		return 1
	}
	return page
}

func normalizeSize(size int, def int) int {
	if size <= 0 {
		return def
	}
	return size
}
