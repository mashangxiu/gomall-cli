package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"gomall-cli/internal/app"
	"gomall-cli/internal/clierr"
	"gomall-cli/internal/model"
)

func newModelCmd() *cobra.Command {
	modelCmd := &cobra.Command{
		Use:   "model",
		Short: "Model related operations",
	}

	modelCmd.AddCommand(newModelSearchCmd())
	modelCmd.AddCommand(newModelCreatedCmd())
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

func newModelDetailCmd() *cobra.Command {
	var showReadme bool

	cmd := &cobra.Command{
		Use:   "detail 作者/名称",
		Short: "Get model detail by author/name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			author, name, err := splitModelRef(args[0])
			if err != nil {
				return clierr.New(clierr.CodeInvalidInput, "参数错误："+err.Error())
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := model.NewService(ctx.APIClient)
			detail, err := svc.Detail(cmd.Context(), author, name)
			if err != nil {
				ctx.Logger.Error("model detail failed", "error", err, "author", author, "name", name)
				return clierr.New(clierr.CodeRuntime, "查询模型详情失败："+err.Error())
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
