package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gomall-cli/internal/modelupload"
)

const defaultAPIBaseURL = "http://10.60.1.140:30591/goMallApi/api"

type syncModelResponse struct {
	Code       int             `json:"code"`
	Message    string          `json:"message"`
	RequestID  string          `json:"request_id"`
	RequestID2 string          `json:"requestId"`
	Data       json.RawMessage `json:"data"`
}

type syncModel struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CNName     string `json:"cn_name"`
	Username   string `json:"username"`
	LabAddress string `json:"lab_address"`
}

type options struct {
	modelDir             string
	apiBaseURL           string
	syncURL              string
	apiTimeout           time.Duration
	lfsTimeout           time.Duration
	apiInsecure          bool
	apiUserAgent         string
	uploadUserAgent      string
	name                 string
	cnName               string
	license              string
	description          string
	visibility           int
	username             string
	taskIDs              string
	deleteExisting       bool
	source               string
	largeFileThresholdMB int
	branch               string
	lfsUploadURLOverride string
	debug                bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseArgs(args)
	if err != nil {
		return err
	}

	modelDir, err := filepath.Abs(opts.modelDir)
	if err != nil {
		return fmt.Errorf("resolve model dir: %w", err)
	}
	stat, err := os.Stat(modelDir)
	if err != nil {
		return fmt.Errorf("stat model dir: %w", err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("model path is not a directory: %s", modelDir)
	}
	opts.modelDir = modelDir

	if opts.name == "" {
		opts.name = filepath.Base(modelDir)
	}
	if opts.description == "" {
		opts.description = "同步上传模型 " + opts.name
	}
	if opts.username == "" {
		opts.username = strings.TrimSpace(os.Getenv("GOMALL_SYNC_USERNAME"))
	}
	if opts.username == "" {
		opts.username = strings.TrimSpace(os.Getenv("USER"))
	}
	if opts.username == "" {
		return errors.New("username is required, pass --username or set GOMALL_SYNC_USERNAME")
	}
	if opts.syncURL == "" {
		opts.syncURL, err = joinURL(opts.apiBaseURL, "synchronization/model")
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "同步模型元数据: name=%s username=%s visibility=%d\n", opts.name, opts.username, opts.visibility)
	model, token, isNew, err := synchronizeModel(ctx, opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("synchronization/model response missing user_token header")
	}
	if strings.TrimSpace(model.LabAddress) == "" {
		return errors.New("synchronization/model response missing data.lab_address")
	}

	fmt.Fprintf(stdout, "模型仓库已准备: id=%d name=%s is_new=%s repo=%s\n", model.ID, model.Name, isNew, model.LabAddress)
	fmt.Fprintf(stdout, "开始上传目录: %s\n", modelDir)
	result, err := modelupload.Upload(ctx, modelupload.Options{
		Dir:                  modelDir,
		RepoURL:              model.LabAddress,
		Token:                token,
		UserAgent:            opts.apiUserAgent,
		Insecure:             opts.apiInsecure,
		Timeout:              opts.lfsTimeout,
		LargeFileThresholdMB: opts.largeFileThresholdMB,
		Branch:               opts.branch,
		Username:             opts.username,
		TransferURLOverride:  opts.lfsUploadURLOverride,
		MergeRemote:          true,
		Debug:                opts.debug,
		DebugOut:             stdout,
		ProgressOut:          stdout,
	})
	if err != nil {
		return fmt.Errorf("upload model files: %w", err)
	}

	fmt.Fprintln(stdout, "上传成功")
	fmt.Fprintf(stdout, "ID: %d\n", model.ID)
	fmt.Fprintf(stdout, "名称: %s\n", model.Name)
	fmt.Fprintf(stdout, "仓库地址: %s\n", model.LabAddress)
	fmt.Fprintf(stdout, "分支: %s\n", result.Branch)
	fmt.Fprintf(stdout, "Commit: %s\n", result.Commit)
	fmt.Fprintf(stdout, "文件数: %d\n", result.Files)
	fmt.Fprintf(stdout, "LFS文件数: %d\n", result.LFSFiles)
	_ = stderr
	return nil
}

func parseArgs(args []string) (options, error) {
	opts := options{}
	fs := flag.NewFlagSet("sync_model_upload", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.apiBaseURL, "api-base-url", envDefault("GOMALL_API_BASE_URL", defaultAPIBaseURL), "gomall API base URL")
	fs.StringVar(&opts.syncURL, "sync-url", envDefault("GOMALL_SYNC_MODEL_URL", ""), "absolute synchronization/model URL")
	fs.DurationVar(&opts.apiTimeout, "api-timeout", 30*time.Second, "synchronization/model HTTP timeout")
	fs.DurationVar(&opts.lfsTimeout, "api-lfs-timeout", 30*time.Minute, "Git LFS upload timeout")
	fs.BoolVar(&opts.apiInsecure, "api-insecure", false, "skip TLS certificate verification")
	fs.StringVar(&opts.apiUserAgent, "api-user-agent", "gomall-cli-sync/0.1.0", "User-Agent for API and LFS requests")
	fs.StringVar(&opts.uploadUserAgent, "upload-user-agent", "gomall-cli-sync/0.1.0", "upload-user-agent header required by synchronization/model")
	fs.StringVar(&opts.name, "name", "", "model name, defaults to folder name")
	fs.StringVar(&opts.cnName, "cn-name", "", "model Chinese name")
	fs.StringVar(&opts.license, "license", "MIT", "model license")
	fs.StringVar(&opts.description, "description", "", "model description")
	fs.IntVar(&opts.visibility, "visibility", 1, "1=private, 5=public")
	fs.StringVar(&opts.username, "username", envDefault("GOMALL_SYNC_USERNAME", ""), "gomall username")
	fs.StringVar(&opts.taskIDs, "task-ids", "", "optional task ids, comma separated")
	fs.BoolVar(&opts.deleteExisting, "delete-existing", false, "delete and recreate existing same-name model")
	fs.StringVar(&opts.source, "source", "批量上传", "model source")
	fs.IntVar(&opts.largeFileThresholdMB, "large-file-threshold-mb", 10, "files greater than or equal to this size use Git LFS")
	fs.StringVar(&opts.branch, "branch", "master", "git branch to push")
	fs.StringVar(&opts.lfsUploadURLOverride, "api-lfs-upload-url-override", "", "force override LFS upload URL base")
	fs.BoolVar(&opts.debug, "debug", false, "print upload debug flow")

	if err := fs.Parse(args); err != nil {
		return options{}, fmt.Errorf("%w\n%s", err, usage())
	}
	if fs.NArg() != 1 {
		return options{}, fmt.Errorf("model directory is required\n%s", usage())
	}
	opts.modelDir = strings.TrimSpace(fs.Arg(0))
	if opts.modelDir == "" {
		return options{}, fmt.Errorf("model directory cannot be empty\n%s", usage())
	}
	if opts.visibility != 1 && opts.visibility != 5 {
		return options{}, errors.New("--visibility only supports 1(private) or 5(public)")
	}
	if opts.largeFileThresholdMB <= 0 {
		return options{}, errors.New("--large-file-threshold-mb must be greater than 0")
	}
	if strings.TrimSpace(opts.license) == "" {
		return options{}, errors.New("--license cannot be empty")
	}
	if strings.TrimSpace(opts.uploadUserAgent) == "" {
		return options{}, errors.New("--upload-user-agent cannot be empty")
	}
	opts.apiBaseURL = strings.TrimSpace(opts.apiBaseURL)
	opts.syncURL = strings.TrimSpace(opts.syncURL)
	opts.name = strings.TrimSpace(opts.name)
	opts.cnName = strings.TrimSpace(opts.cnName)
	opts.license = strings.TrimSpace(opts.license)
	opts.description = strings.TrimSpace(opts.description)
	opts.username = strings.TrimSpace(opts.username)
	opts.taskIDs = strings.TrimSpace(opts.taskIDs)
	opts.source = strings.TrimSpace(opts.source)
	opts.branch = strings.TrimSpace(opts.branch)
	opts.lfsUploadURLOverride = strings.TrimSpace(opts.lfsUploadURLOverride)
	if opts.branch == "" {
		opts.branch = "master"
	}
	return opts, nil
}

func synchronizeModel(ctx context.Context, opts options) (syncModel, string, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"name":            opts.name,
		"license":         opts.license,
		"description":     opts.description,
		"visibility":      strconv.Itoa(opts.visibility),
		"username":        opts.username,
		"delete_existing": strconv.FormatBool(opts.deleteExisting),
		"source":          opts.source,
	}
	if opts.cnName != "" {
		fields["cn_name"] = opts.cnName
	}
	if opts.taskIDs != "" {
		fields["task_ids"] = opts.taskIDs
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return syncModel{}, "", "", fmt.Errorf("write multipart field %q: %w", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		return syncModel{}, "", "", fmt.Errorf("finalize multipart body: %w", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.apiInsecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: opts.apiTimeout, Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.syncURL, &body)
	if err != nil {
		return syncModel{}, "", "", fmt.Errorf("build synchronization request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", opts.apiUserAgent)
	req.Header.Set("upload-user-agent", opts.uploadUserAgent)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return syncModel{}, "", "", fmt.Errorf("call synchronization/model: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return syncModel{}, "", "", fmt.Errorf("read synchronization/model response: %w", err)
	}
	var env syncModelResponse
	if err := json.Unmarshal(respBody, &env); err != nil {
		return syncModel{}, "", "", fmt.Errorf("decode synchronization/model response status=%d: %w body=%s", resp.StatusCode, err, string(respBody))
	}
	if resp.StatusCode >= 400 || env.Code != 200 {
		return syncModel{}, "", "", fmt.Errorf("synchronization/model failed: http=%d code=%d message=%s request_id=%s", resp.StatusCode, env.Code, env.Message, effectiveRequestID(env))
	}

	var model syncModel
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &model); err != nil {
			return syncModel{}, "", "", fmt.Errorf("decode synchronization/model data: %w", err)
		}
	}
	return model, resp.Header.Get("user_token"), resp.Header.Get("is_new"), nil
}

func joinURL(base, elem string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("--api-base-url cannot be empty")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse --api-base-url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(elem, "/")
	return u.String(), nil
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func effectiveRequestID(env syncModelResponse) string {
	if env.RequestID != "" {
		return env.RequestID
	}
	return env.RequestID2
}

func usage() string {
	return `Usage:
  gomall-sync-model-upload [flags] MODEL_DIR

Examples:
  gomall-sync-model-upload --username fanze ./demomodel
  gomall-sync-model-upload --username fanze --visibility 5 --delete-existing ./demomodel`
}
