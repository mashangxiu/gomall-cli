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
const createPath = "/goMallApi/api/v2/models/create"
const deletePathPrefix = "/goMallApi/api/v2/models"

// Service handles model-related API operations.
type Service struct {
	client *gomallapi.Client
}

type APIError struct {
	Operation string
	Code      int
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.RequestID) != "" {
		return fmt.Sprintf("%s failed: code=%d message=%s request_id=%s", e.Operation, e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("%s failed: code=%d message=%s", e.Operation, e.Code, e.Message)
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

type CreateOptions struct {
	Name        string
	CNName      string
	License     string
	Description string
	Visibility  int
	TaskIDs     string
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

type CreatedModel struct {
	ID               int64  `json:"id"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
	Name             string `json:"name"`
	CNName           string `json:"cn_name"`
	Description      string `json:"description"`
	Username         string `json:"username"`
	License          string `json:"license"`
	LabAddress       string `json:"lab_address"`
	VisibilityStatus int    `json:"visibility_status"`
	GitlabID         int64  `json:"gitlab_id"`
	Source           string `json:"source"`
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
		return ModelDetail{}, apiError("model detail", env)
	}

	var detail ModelDetail
	if err := env.DecodeData(&detail); err != nil {
		return ModelDetail{}, err
	}
	return detail, nil
}

func (s *Service) DetailByID(ctx context.Context, id int64) (ModelDetail, error) {
	if id <= 0 {
		return ModelDetail{}, fmt.Errorf("id must be greater than 0")
	}

	path := buildDetailByIDPath(id)
	env, err := s.client.Do(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return ModelDetail{}, err
	}
	if env.Code != 200 {
		return ModelDetail{}, apiError("model detail", env)
	}

	var detail ModelDetail
	if err := env.DecodeData(&detail); err != nil {
		return ModelDetail{}, err
	}
	return detail, nil
}

func (s *Service) Create(ctx context.Context, opts CreateOptions) (CreatedModel, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return CreatedModel{}, fmt.Errorf("name cannot be empty")
	}

	visibility := opts.Visibility
	if visibility == 0 {
		visibility = 1
	}
	if visibility != 1 && visibility != 5 {
		return CreatedModel{}, fmt.Errorf("visibility must be 1(private) or 5(public)")
	}

	license := strings.TrimSpace(opts.License)
	if license == "" {
		license = "MIT"
	}

	cnName := strings.TrimSpace(opts.CNName)
	if cnName == "" {
		cnName = name
	}

	fields := map[string]string{
		"name":        name,
		"cn_name":     cnName,
		"license":     license,
		"description": strings.TrimSpace(opts.Description),
		"visibility":  strconv.Itoa(visibility),
		"task_ids":    strings.TrimSpace(opts.TaskIDs),
	}

	env, err := s.client.DoMultipartForm(ctx, http.MethodPost, createPath, fields, true)
	if err != nil {
		return CreatedModel{}, err
	}
	if env.Code != 200 {
		return CreatedModel{}, apiError("model create", env)
	}

	var created CreatedModel
	if err := env.DecodeData(&created); err != nil {
		return CreatedModel{}, err
	}
	return created, nil
}

func (s *Service) DeleteByID(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("id must be greater than 0")
	}
	path := buildDeleteByIDPath(id)
	env, err := s.client.Do(ctx, http.MethodDelete, path, nil, true)
	if err != nil {
		return err
	}
	if env.Code != 200 {
		return apiError("model delete", env)
	}
	return nil
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

func buildDetailByIDPath(id int64) string {
	return detailPathPrefix + "/" + strconv.FormatInt(id, 10)
}

func buildDeleteByIDPath(id int64) string {
	return deletePathPrefix + "/" + strconv.FormatInt(id, 10)
}

func decodeSearchResult(env gomallapi.Envelope) (SearchResult, error) {
	if env.Code != 200 {
		return SearchResult{}, apiError("model search", env)
	}
	var result SearchResult
	if err := env.DecodeData(&result); err != nil {
		return SearchResult{}, err
	}
	return result, nil
}

func apiError(operation string, env gomallapi.Envelope) error {
	return &APIError{
		Operation: operation,
		Code:      env.Code,
		Message:   env.Message,
		RequestID: env.RequestID,
	}
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
