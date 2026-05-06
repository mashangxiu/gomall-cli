package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/spf13/cobra"

	"gomall-cli/internal/app"
	"gomall-cli/internal/clierr"
	"gomall-cli/internal/gitlfs"
	"gomall-cli/internal/model"
	"gomall-cli/internal/modelupload"
)

func newModelCmd() *cobra.Command {
	modelCmd := &cobra.Command{
		Use:   "model",
		Short: "Model related operations",
	}

	modelCmd.AddCommand(newModelSearchCmd())
	modelCmd.AddCommand(newModelCreatedCmd())
	modelCmd.AddCommand(newModelCreateCmd())
	modelCmd.AddCommand(newModelDeleteCmd())
	modelCmd.AddCommand(newModelUploadCmd())
	modelCmd.AddCommand(newModelCloneCmd())
	modelCmd.AddCommand(newModelDetailCmd())
	return modelCmd
}

func newModelSearchCmd() *cobra.Command {
	var name string
	var page int
	var size int

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search public models by name",
		RunE: func(cmd *cobra.Command, args []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--name 不能为空")
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			result, err := svc.Search(cmd.Context(), model.SearchOptions{
				Name: name,
				Page: page,
				Size: size,
			})
			if err != nil {
				ctx.Logger.Error("model search failed", "error", err, "name", name)
				return clierr.New(clierr.CodeRuntime, "模型检索失败："+err.Error())
			}

			fmt.Printf("检索成功：共 %d 条，当前第 %d/%d 页\n", result.Total, result.Page, result.TotalPages)
			if len(result.Items) == 0 {
				fmt.Println("未检索到模型")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\t名称\t作者\t创建时间")
			for _, item := range result.Items {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					item.ID,
					item.Name,
					item.Username,
					formatShanghaiTime(item.CreatedAt),
				)
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "model name keyword (required)")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().IntVar(&size, "size", 10, "page size")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newModelCreatedCmd() *cobra.Command {
	var name string
	var page int
	var size int

	cmd := &cobra.Command{
		Use:   "created",
		Short: "List models created by current user",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			result, err := svc.Created(cmd.Context(), model.CreatedOptions{
				Name: strings.TrimSpace(name),
				Page: page,
				Size: size,
			})
			if err != nil {
				ctx.Logger.Error("model created list failed", "error", err, "name", name)
				return clierr.New(clierr.CodeRuntime, "查询我创建的模型失败："+err.Error())
			}

			fmt.Printf("查询成功：共 %d 条，当前第 %d/%d 页\n", result.Total, result.Page, result.TotalPages)
			if len(result.Items) == 0 {
				fmt.Println("未查询到模型")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\t名称\t作者\t创建时间")
			for _, item := range result.Items {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					item.ID,
					item.Name,
					item.Username,
					formatShanghaiTime(item.CreatedAt),
				)
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "optional model name keyword")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().IntVar(&size, "size", 16, "page size")
	return cmd
}

func newModelCreateCmd() *cobra.Command {
	var name string
	var cnName string
	var license string
	var description string
	var visibility int
	var taskIDs string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a model",
		RunE: func(cmd *cobra.Command, args []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--name 不能为空")
			}
			if visibility != 1 && visibility != 5 {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--visibility 仅支持 1(私有) 或 5(公开)")
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			createCtx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()

			created, err := svc.Create(createCtx, model.CreateOptions{
				Name:        name,
				CNName:      strings.TrimSpace(cnName),
				License:     strings.TrimSpace(license),
				Description: strings.TrimSpace(description),
				Visibility:  visibility,
				TaskIDs:     strings.TrimSpace(taskIDs),
			})
			if err != nil {
				ctx.Logger.Error("model create failed", "error", err, "name", name, "visibility", visibility)
				return clierr.New(clierr.CodeRuntime, "创建模型失败："+err.Error())
			}

			fmt.Println("创建成功")
			fmt.Printf("ID: %d\n", created.ID)
			fmt.Printf("作者: %s\n", created.Username)
			fmt.Printf("名称: %s\n", created.Name)
			if strings.TrimSpace(created.CNName) != "" {
				fmt.Printf("中文名: %s\n", created.CNName)
			}
			fmt.Printf("可见性: %s\n", visibilityText(created.VisibilityStatus))
			if strings.TrimSpace(created.License) != "" {
				fmt.Printf("许可证: %s\n", created.License)
			}
			fmt.Printf("创建时间: %s\n", formatShanghaiTime(created.CreatedAt))
			if strings.TrimSpace(created.LabAddress) != "" {
				fmt.Printf("仓库地址: %s\n", created.LabAddress)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "model name (required)")
	cmd.Flags().StringVar(&cnName, "cn-name", "", "model chinese name (default to --name)")
	cmd.Flags().StringVar(&license, "license", "MIT", "model license")
	cmd.Flags().StringVar(&description, "description", "", "model description")
	cmd.Flags().IntVar(&visibility, "visibility", 1, "1=private, 5=public")
	cmd.Flags().StringVar(&taskIDs, "task-ids", "", "optional task ids, comma separated")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newModelDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete a model by numeric ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := tryParsePositiveInt64(strings.TrimSpace(args[0]))
			if !ok {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：ID 必须是大于 0 的整数")
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			if !yes {
				confirmed, confirmErr := confirmDelete(cmd, id)
				if confirmErr != nil {
					return clierr.New(clierr.CodeRuntime, "读取确认输入失败："+confirmErr.Error())
				}
				if !confirmed {
					fmt.Println("已取消删除")
					return nil
				}
			}

			svc := model.NewService(ctx.APIClient)
			deleteCtx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()

			if err := svc.DeleteByID(deleteCtx, id); err != nil {
				ctx.Logger.Error("model delete failed", "error", err, "id", id)
				return clierr.New(clierr.CodeRuntime, "删除模型失败："+err.Error())
			}

			fmt.Println("删除成功")
			fmt.Printf("ID: %d\n", id)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation")
	return cmd
}

func newModelUploadCmd() *cobra.Command {
	var cnName string
	var license string
	var description string
	var visibility int
	var taskIDs string
	var thresholdMB int
	var branch string
	var modelRef string
	var debug bool
	var replaceIfExists bool
	var useIfExists bool

	cmd := &cobra.Command{
		Use:   "upload FOLDER",
		Short: "Upload a local folder to a new or existing model repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := strings.TrimSpace(args[0])
			if folder == "" {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：FOLDER 不能为空")
			}
			if visibility != 1 && visibility != 5 {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--visibility 仅支持 1(私有) 或 5(公开)")
			}
			if thresholdMB <= 0 {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--large-file-threshold-mb 必须大于 0")
			}
			if replaceIfExists && useIfExists {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：--replace-if-exists 和 --use-if-exists 不能同时使用")
			}

			absFolder, err := filepath.Abs(folder)
			if err != nil {
				return clierr.New(clierr.CodeInvalidInput, "解析目录失败："+err.Error())
			}
			stat, err := os.Stat(absFolder)
			if err != nil {
				return clierr.New(clierr.CodeInvalidInput, "读取目录失败："+err.Error())
			}
			if !stat.IsDir() {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：FOLDER 必须是目录")
			}
			name := filepath.Base(absFolder)
			if strings.TrimSpace(name) == "" || name == "." || name == string(filepath.Separator) {
				return clierr.New(clierr.CodeInvalidInput, "参数错误：无法从目录名推导模型名称")
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}
			sess, err := ctx.SessionStore.Load()
			if err != nil {
				return clierr.New(clierr.CodeRuntime, "读取本地登录态失败："+err.Error())
			}
			gitlabToken := strings.TrimSpace(sess.GitlabToken)
			if gitlabToken == "" {
				return clierr.New(clierr.CodeRuntime, "当前登录态缺少 GitLab 认证信息，请先重新执行 gomall-cli auth login")
			}

			svc := model.NewService(ctx.APIClient)
			target, err := resolveUploadTarget(cmd.Context(), svc, uploadTargetOptions{
				ModelRef:        strings.TrimSpace(modelRef),
				Name:            name,
				CNName:          cnName,
				License:         license,
				Description:     description,
				Visibility:      visibility,
				TaskIDs:         taskIDs,
				ReplaceIfExists: replaceIfExists,
				UseIfExists:     useIfExists,
				Debug:           debug,
				Out:             cmd.OutOrStdout(),
			})
			if err != nil {
				ctx.Logger.Error("model upload target resolve failed", "error", err, "name", name, "model_ref", modelRef)
				return clierr.New(clierr.CodeRuntime, "准备上传模型失败："+err.Error())
			}
			if strings.TrimSpace(target.LabAddress) == "" {
				return clierr.New(clierr.CodeRuntime, "模型仓库地址为空，无法上传")
			}
			if debug {
				fmt.Fprintf(cmd.OutOrStdout(), "[DEBUG] upload target: id=%d name=%s repo=%s model_ref=%s\n", target.ID, target.Name, target.LabAddress, strings.TrimSpace(modelRef))
			}

			fmt.Printf("开始上传目录: %s\n", absFolder)
			result, err := modelupload.Upload(cmd.Context(), modelupload.Options{
				Dir:                  absFolder,
				RepoURL:              target.LabAddress,
				Token:                gitlabToken,
				UserAgent:            ctx.Config.API.UserAgent,
				Insecure:             ctx.Config.API.Insecure,
				Timeout:              ctx.Config.API.LFSTimeout,
				LargeFileThresholdMB: thresholdMB,
				Branch:               strings.TrimSpace(branch),
				Username:             sess.Username,
				TransferURLOverride:  ctx.Config.API.LFSUploadURLOverride,
				MergeRemote:          true,
				Debug:                debug,
				DebugOut:             cmd.OutOrStdout(),
				ProgressOut:          cmd.OutOrStdout(),
			})
			if err != nil {
				ctx.Logger.Error("model upload failed", "error", err, "name", target.Name, "repo_url", target.LabAddress)
				return clierr.New(clierr.CodeRuntime, "上传模型失败："+err.Error())
			}

			fmt.Println("上传成功")
			fmt.Printf("ID: %d\n", target.ID)
			fmt.Printf("名称: %s\n", target.Name)
			fmt.Printf("仓库地址: %s\n", target.LabAddress)
			fmt.Printf("分支: %s\n", result.Branch)
			fmt.Printf("Commit: %s\n", result.Commit)
			fmt.Printf("文件数: %d\n", result.Files)
			fmt.Printf("LFS文件数: %d\n", result.LFSFiles)
			return nil
		},
	}

	cmd.Flags().StringVar(&cnName, "cn-name", "", "model chinese name (default to folder name)")
	cmd.Flags().StringVar(&license, "license", "MIT", "model license")
	cmd.Flags().StringVar(&description, "description", "", "model description")
	cmd.Flags().IntVar(&visibility, "visibility", 1, "1=private, 5=public")
	cmd.Flags().StringVar(&taskIDs, "task-ids", "", "optional task ids, comma separated")
	cmd.Flags().IntVar(&thresholdMB, "large-file-threshold-mb", 10, "files greater than or equal to this size use Git LFS")
	cmd.Flags().StringVar(&branch, "branch", "master", "git branch to push")
	cmd.Flags().StringVar(&modelRef, "model", "", "existing model reference, supports ID or 作者/名称; empty means create a new model from folder name")
	cmd.Flags().BoolVar(&debug, "debug", false, "print model upload debug flow, LFS batch payloads and HTTP responses")
	cmd.Flags().BoolVar(&replaceIfExists, "replace-if-exists", false, "delete and recreate the current user's same-name model when creating a new upload target")
	cmd.Flags().BoolVar(&useIfExists, "use-if-exists", false, "reuse the current user's same-name model repository when creating a new upload target")
	return cmd
}

type uploadTarget struct {
	ID         int64
	Name       string
	LabAddress string
}

type uploadTargetOptions struct {
	ModelRef        string
	Name            string
	CNName          string
	License         string
	Description     string
	Visibility      int
	TaskIDs         string
	ReplaceIfExists bool
	UseIfExists     bool
	Debug           bool
	Out             io.Writer
}

func resolveUploadTarget(ctx context.Context, svc *model.Service, opts uploadTargetOptions) (uploadTarget, error) {
	modelRef := strings.TrimSpace(opts.ModelRef)
	if modelRef == "" {
		created, err := createUploadTarget(ctx, svc, opts)
		if err != nil {
			if opts.ReplaceIfExists && isModelAlreadyExistsError(err) {
				return replaceExistingUploadTarget(ctx, svc, opts)
			}
			if opts.UseIfExists && isModelAlreadyExistsError(err) {
				return useExistingUploadTarget(ctx, svc, opts)
			}
			return uploadTarget{}, fmt.Errorf("创建模型失败：%w", err)
		}
		return uploadTarget{ID: created.ID, Name: created.Name, LabAddress: created.LabAddress}, nil
	}

	fmt.Printf("查询已有模型: %s\n", modelRef)
	var detail model.ModelDetail
	var err error
	if id, ok := tryParsePositiveInt64(modelRef); ok {
		detail, err = svc.DetailByID(ctx, id)
	} else {
		author, modelName, parseErr := splitModelRef(modelRef)
		if parseErr != nil {
			return uploadTarget{}, fmt.Errorf("参数错误：--model 必须是 ID 或 作者/名称")
		}
		detail, err = svc.Detail(ctx, author, modelName)
	}
	if err != nil {
		return uploadTarget{}, fmt.Errorf("查询已有模型失败：%w", err)
	}
	return uploadTarget{ID: detail.ID, Name: detail.Name, LabAddress: detail.LabAddress}, nil
}

func createUploadTarget(ctx context.Context, svc *model.Service, opts uploadTargetOptions) (model.CreatedModel, error) {
	createCtx, cancelCreate := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelCreate()

	fmt.Printf("开始创建模型: %s\n", strings.TrimSpace(opts.Name))
	return svc.Create(createCtx, model.CreateOptions{
		Name:        strings.TrimSpace(opts.Name),
		CNName:      strings.TrimSpace(opts.CNName),
		License:     strings.TrimSpace(opts.License),
		Description: strings.TrimSpace(opts.Description),
		Visibility:  opts.Visibility,
		TaskIDs:     strings.TrimSpace(opts.TaskIDs),
	})
}

func replaceExistingUploadTarget(ctx context.Context, svc *model.Service, opts uploadTargetOptions) (uploadTarget, error) {
	name := strings.TrimSpace(opts.Name)
	fmt.Printf("模型已存在，开始删除并重新创建: %s\n", name)
	existing, err := findCreatedModelByExactName(ctx, svc, name)
	if err != nil {
		return uploadTarget{}, fmt.Errorf("查找已存在模型失败：%w", err)
	}
	if existing.ID <= 0 {
		return uploadTarget{}, fmt.Errorf("模型已存在，但没有在当前用户创建的模型列表中找到同名模型：%s", name)
	}
	if opts.Debug {
		fmt.Fprintf(opts.Out, "[DEBUG] replace existing model: id=%d name=%s repo=%s\n", existing.ID, existing.Name, existing.LabAddress)
	}

	deleteCtx, cancelDelete := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelDelete()
	if err := svc.DeleteByID(deleteCtx, existing.ID); err != nil {
		return uploadTarget{}, fmt.Errorf("删除已存在模型失败：%w", err)
	}
	fmt.Printf("已删除旧模型: ID=%d\n", existing.ID)

	created, err := createUploadTarget(ctx, svc, opts)
	if err != nil {
		return uploadTarget{}, fmt.Errorf("重新创建模型失败：%w", err)
	}
	return uploadTarget{ID: created.ID, Name: created.Name, LabAddress: created.LabAddress}, nil
}

func useExistingUploadTarget(ctx context.Context, svc *model.Service, opts uploadTargetOptions) (uploadTarget, error) {
	name := strings.TrimSpace(opts.Name)
	fmt.Printf("模型已存在，使用已有模型仓库继续上传: %s\n", name)
	existing, err := findCreatedModelByExactName(ctx, svc, name)
	if err != nil {
		return uploadTarget{}, fmt.Errorf("查找已存在模型失败：%w", err)
	}
	if existing.ID <= 0 {
		return uploadTarget{}, fmt.Errorf("模型已存在，但没有在当前用户创建的模型列表中找到同名模型：%s", name)
	}
	if strings.TrimSpace(existing.LabAddress) == "" {
		return uploadTarget{}, fmt.Errorf("已存在模型仓库地址为空：%s", name)
	}
	if opts.Debug {
		fmt.Fprintf(opts.Out, "[DEBUG] use existing model: id=%d name=%s repo=%s\n", existing.ID, existing.Name, existing.LabAddress)
	}
	return uploadTarget{ID: existing.ID, Name: existing.Name, LabAddress: existing.LabAddress}, nil
}

func findCreatedModelByExactName(ctx context.Context, svc *model.Service, name string) (model.ModelItem, error) {
	result, err := svc.Created(ctx, model.CreatedOptions{Name: name, Page: 1, Size: 100})
	if err != nil {
		return model.ModelItem{}, err
	}
	for _, item := range result.Items {
		if item.Name == name {
			return item, nil
		}
	}
	return model.ModelItem{}, nil
}

func isModelAlreadyExistsError(err error) bool {
	var apiErr *model.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 10145 {
		return true
	}
	return strings.Contains(err.Error(), "code=10145") || strings.Contains(err.Error(), "模型已经存在")
}

func confirmDelete(cmd *cobra.Command, id int64) (bool, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "确认删除模型 ID=%d ? [y/N]: ", id)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return isYesInput(line), nil
}

func isYesInput(input string) bool {
	v := strings.ToLower(strings.TrimSpace(input))
	return v == "y" || v == "yes"
}

func newModelCloneCmd() *cobra.Command {
	var into string
	var dir string
	var debugLFSBatch bool

	cmd := &cobra.Command{
		Use:   "clone 作者/名称|ID",
		Short: "Clone model repository to local directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			input := strings.TrimSpace(args[0])
			var detail model.ModelDetail
			if id, ok := tryParsePositiveInt64(input); ok {
				detail, err = svc.DetailByID(cmd.Context(), id)
				if err != nil {
					ctx.Logger.Error("model detail by id failed before clone", "error", err, "id", id)
					return clierr.New(clierr.CodeRuntime, "获取模型详情失败："+err.Error())
				}
			} else {
				author, name, parseErr := splitModelRef(input)
				if parseErr != nil {
					return clierr.New(clierr.CodeInvalidInput, "参数错误："+parseErr.Error())
				}
				detail, err = svc.Detail(cmd.Context(), author, name)
				if err != nil {
					ctx.Logger.Error("model detail failed before clone", "error", err, "author", author, "name", name)
					return clierr.New(clierr.CodeRuntime, "获取模型详情失败："+err.Error())
				}
			}
			repoURL := strings.TrimSpace(detail.LabAddress)
			if repoURL == "" {
				return clierr.New(clierr.CodeRuntime, "模型仓库地址为空，无法克隆")
			}

			targetDir, err := resolveCloneTarget(repoURL, detail.Name, into, dir)
			if err != nil {
				return clierr.New(clierr.CodeInvalidInput, "参数错误："+err.Error())
			}
			resumeMode := false
			if stat, statErr := os.Stat(targetDir); statErr == nil {
				if !stat.IsDir() {
					return clierr.New(clierr.CodeInvalidInput, "目标路径已存在且不是目录："+targetDir)
				}
				if err := ensureResumableRepo(targetDir, repoURL); err != nil {
					return clierr.New(clierr.CodeInvalidInput, err.Error())
				}
				resumeMode = true
			} else if !os.IsNotExist(statErr) {
				return clierr.New(clierr.CodeRuntime, "检查目标目录失败："+statErr.Error())
			}
			if !resumeMode {
				if mkErr := os.MkdirAll(filepath.Dir(targetDir), 0o755); mkErr != nil {
					return clierr.New(clierr.CodeRuntime, "创建父目录失败："+mkErr.Error())
				}
			}

			sess, err := ctx.SessionStore.Load()
			if err != nil {
				return clierr.New(clierr.CodeRuntime, "读取本地登录态失败："+err.Error())
			}
			gitlabToken := strings.TrimSpace(sess.GitlabToken)
			if gitlabToken == "" {
				return clierr.New(
					clierr.CodeRuntime,
					"当前登录态缺少 GitLab 认证信息，请先重新执行 gomall-cli auth login",
				)
			}

			if resumeMode {
				fmt.Printf("目标目录已存在，进入断点续传模式: %s\n", targetDir)
				if err := syncResumableRepo(cmd.Context(), targetDir, &githttp.BasicAuth{
					Username: "oauth2",
					Password: gitlabToken,
				}, cmd.OutOrStdout()); err != nil {
					ctx.Logger.Error("sync existing repo failed", "error", err, "target_dir", targetDir)
					return clierr.New(clierr.CodeRuntime, "补全普通 Git 文件失败："+err.Error())
				}
			} else {
				fmt.Printf("开始克隆: %s\n", repoURL)
				fmt.Printf("目标目录: %s\n", targetDir)
				_, err = gogit.PlainCloneContext(cmd.Context(), targetDir, false, &gogit.CloneOptions{
					URL:      repoURL,
					Auth:     &githttp.BasicAuth{Username: "oauth2", Password: gitlabToken},
					Progress: cmd.OutOrStdout(),
				})
				if err != nil {
					ctx.Logger.Error("model clone failed", "error", err, "repo_url", repoURL, "target_dir", targetDir)
					return clierr.New(clierr.CodeRuntime, "克隆失败："+err.Error())
				}
			}

			fmt.Println("开始补全 Git LFS 大文件...")
			hydrated, err := gitlfs.Hydrate(cmd.Context(), gitlfs.HydrateOptions{
				RepoDir:             targetDir,
				RepoURL:             repoURL,
				Token:               gitlabToken,
				UserAgent:           ctx.Config.API.UserAgent,
				Insecure:            ctx.Config.API.Insecure,
				HTTPTimeout:         ctx.Config.API.LFSTimeout,
				IdleTimeout:         ctx.Config.API.LFSIdleTimeout,
				ChunkSize:           int64(ctx.Config.API.LFSChunkSizeMB) * 1024 * 1024,
				DownloadURLOverride: ctx.Config.API.LFSDownloadURLOverride,
				ProgressOut:         cmd.OutOrStdout(),
				DebugBatch:          debugLFSBatch,
				DebugOut:            cmd.ErrOrStderr(),
			})
			if err != nil {
				ctx.Logger.Error("git lfs hydrate failed", "error", err, "repo_url", repoURL, "target_dir", targetDir)
				return clierr.New(clierr.CodeRuntime, "Git LFS 大文件补全失败："+err.Error())
			}
			if hydrated == 0 {
				fmt.Println("Git LFS: 未发现需要补全的大文件")
			} else {
				fmt.Printf("Git LFS 补全成功：%d 个文件\n", hydrated)
			}

			fmt.Println("克隆成功")
			fmt.Printf("本地路径: %s\n", targetDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&into, "into", ".", "parent directory to place cloned repository")
	cmd.Flags().StringVar(&dir, "dir", "", "explicit target directory, overrides --into")
	cmd.Flags().BoolVar(&debugLFSBatch, "debug-lfs-batch", false, "print raw LFS Batch API response for debugging")
	return cmd
}

func newModelDetailCmd() *cobra.Command {
	var showReadme bool

	cmd := &cobra.Command{
		Use:   "detail 作者/名称|ID",
		Short: "Get model detail by author/name or numeric ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			input := strings.TrimSpace(args[0])
			var detail model.ModelDetail
			if id, ok := tryParsePositiveInt64(input); ok {
				detail, err = svc.DetailByID(cmd.Context(), id)
				if err != nil {
					ctx.Logger.Error("model detail by id failed", "error", err, "id", id)
					return clierr.New(clierr.CodeRuntime, "查询模型详情失败："+err.Error())
				}
			} else {
				author, name, parseErr := splitModelRef(input)
				if parseErr != nil {
					return clierr.New(clierr.CodeInvalidInput, "参数错误："+parseErr.Error())
				}
				detail, err = svc.Detail(cmd.Context(), author, name)
				if err != nil {
					ctx.Logger.Error("model detail failed", "error", err, "author", author, "name", name)
					return clierr.New(clierr.CodeRuntime, "查询模型详情失败："+err.Error())
				}
			}

			fmt.Println("查询成功")
			fmt.Printf("ID: %d\n", detail.ID)
			fmt.Printf("作者: %s\n", detail.Username)
			fmt.Printf("名称: %s\n", detail.Name)
			if strings.TrimSpace(detail.CNName) != "" {
				fmt.Printf("中文名: %s\n", detail.CNName)
			}
			fmt.Printf("创建时间: %s\n", formatShanghaiTime(detail.CreatedAt))
			if strings.TrimSpace(detail.Source) != "" {
				fmt.Printf("来源: %s\n", detail.Source)
			}
			if strings.TrimSpace(detail.License) != "" {
				fmt.Printf("许可证: %s\n", detail.License)
			}
			if strings.TrimSpace(detail.LabAddress) != "" {
				fmt.Printf("仓库地址: %s\n", detail.LabAddress)
			}
			if strings.TrimSpace(detail.Description) != "" {
				fmt.Printf("描述: %s\n", detail.Description)
			}
			if showReadme && strings.TrimSpace(detail.Readme) != "" {
				fmt.Printf("\nREADME:\n%s\n", detail.Readme)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showReadme, "show-readme", false, "show readme content in output")
	return cmd
}

func formatShanghaiTime(unixSec int64) string {
	if unixSec <= 0 {
		return "-"
	}
	cst := time.FixedZone("CST", 8*3600)
	return time.Unix(unixSec, 0).In(cst).Format("2006-01-02 15:04:05")
}

func visibilityText(v int) string {
	switch v {
	case 1:
		return "私有(1)"
	case 5:
		return "公开(5)"
	default:
		return strconv.Itoa(v)
	}
}

func splitModelRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("请使用格式 作者/名称，例如 gomall/test1")
	}
	author := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if author == "" || name == "" {
		return "", "", fmt.Errorf("作者和名称都不能为空")
	}
	return author, name, nil
}

func resolveCloneTarget(repoURL, modelName, into, dir string) (string, error) {
	if strings.TrimSpace(dir) != "" {
		return filepath.Clean(strings.TrimSpace(dir)), nil
	}

	into = strings.TrimSpace(into)
	if into == "" {
		into = "."
	}
	base := deriveRepoDirName(repoURL, modelName)
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("无法从仓库地址推断目录名，请使用 --dir 指定")
	}
	return filepath.Join(filepath.Clean(into), base), nil
}

func deriveRepoDirName(repoURL, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err == nil {
		seg := pathpkg.Base(u.Path)
		seg = strings.TrimSuffix(seg, ".git")
		seg = strings.TrimSpace(seg)
		if seg != "" && seg != "." && seg != "/" {
			return seg
		}
	}
	fallback = strings.TrimSpace(fallback)
	return strings.TrimSuffix(fallback, ".git")
}

func ensureResumableRepo(targetDir, expectedRepoURL string) error {
	repo, err := gogit.PlainOpen(targetDir)
	if err != nil {
		return fmt.Errorf("目标目录已存在但不是可续传的 Git 仓库，请更换目录或先清理该目录: %s", targetDir)
	}
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("读取目标仓库配置失败: %w", err)
	}
	origin, ok := cfg.Remotes[gogit.DefaultRemoteName]
	if !ok || len(origin.URLs) == 0 {
		return fmt.Errorf("目标仓库缺少 origin 远程地址，无法确认是否可续传: %s", targetDir)
	}
	if !sameRepoURL(origin.URLs[0], expectedRepoURL) {
		return fmt.Errorf("目标目录仓库与当前模型仓库不一致，请使用 --dir 指定新目录")
	}
	return nil
}

func sameRepoURL(a, b string) bool {
	normalize := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		s = strings.TrimSuffix(s, "/")
		s = strings.TrimSuffix(s, ".git")
		s = strings.TrimSuffix(s, "/")
		return s
	}
	return normalize(a) == normalize(b)
}

func syncResumableRepo(ctx context.Context, targetDir string, auth *githttp.BasicAuth, out io.Writer) error {
	repo, err := gogit.PlainOpen(targetDir)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	if out != nil {
		fmt.Fprintln(out, "开始补全普通 Git 文件...")
	}

	fetchErr := repo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: gogit.DefaultRemoteName,
		Auth:       auth,
		Progress:   out,
		Force:      true,
		Tags:       gogit.NoTags,
	})
	if fetchErr != nil && !errors.Is(fetchErr, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch failed: %w", fetchErr)
	}

	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("read head: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return fmt.Errorf("load head commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("load head tree: %w", err)
	}
	restored, err := restoreMissingTrackedFiles(targetDir, tree)
	if err != nil {
		return fmt.Errorf("restore missing tracked files: %w", err)
	}

	if out != nil {
		fmt.Fprintf(out, "普通 Git 文件补全完成（恢复 %d 个缺失文件）\n", restored)
	}
	return nil
}

func restoreMissingTrackedFiles(repoDir string, tree *object.Tree) (int, error) {
	if tree == nil {
		return 0, fmt.Errorf("nil tree")
	}
	restored := 0
	err := tree.Files().ForEach(func(f *object.File) error {
		if f == nil {
			return nil
		}
		absPath := filepath.Join(repoDir, filepath.FromSlash(f.Name))
		if _, err := os.Lstat(absPath); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return err
		}
		content, err := f.Contents()
		if err != nil {
			return err
		}

		switch f.Mode {
		case filemode.Symlink:
			if err := os.Symlink(content, absPath); err != nil && !os.IsExist(err) {
				return err
			}
		default:
			perm := os.FileMode(0o644)
			if f.Mode == filemode.Executable {
				perm = 0o755
			}
			if err := os.WriteFile(absPath, []byte(content), perm); err != nil {
				return err
			}
			if err := os.Chmod(absPath, perm); err != nil {
				return err
			}
		}
		restored++
		return nil
	})
	if err != nil {
		return restored, err
	}
	return restored, nil
}

func tryParsePositiveInt64(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
