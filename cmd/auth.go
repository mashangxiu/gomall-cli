package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gomall-cli/internal/app"
	"gomall-cli/internal/auth"
	"gomall-cli/internal/clierr"
)

func newAuthCmd() *cobra.Command {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication commands",
	}

	authCmd.AddCommand(newAuthLoginCmd())
	authCmd.AddCommand(newAuthStatusCmd())
	authCmd.AddCommand(newAuthLogoutCmd())

	return authCmd
}

func newAuthLoginCmd() *cobra.Command {
	var username string
	var password string
	var passwordStdin bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Login and persist token/expireTime/username",
		RunE: func(cmd *cobra.Command, args []string) error {
			if passwordStdin {
				p, err := readPasswordFromStdin()
				if err != nil {
					return clierr.Wrap(clierr.CodeInvalidInput, "failed to read password from stdin", err)
				}
				password = p
			}

			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			svc := auth.NewService(ctx.APIClient, ctx.SessionStore, ctx.Config.Auth)
			sess, err := svc.Login(cmd.Context(), username, password)
			if err != nil {
				var loginErr *auth.LoginFailureError
				if errors.As(err, &loginErr) {
					attrs := []any{
						"code", loginErr.Code,
						"message", loginErr.Message,
						"username", username,
					}
					if strings.TrimSpace(loginErr.RequestID) != "" {
						attrs = append(attrs, "request_id", loginErr.RequestID)
					}
					ctx.Logger.Warn("login rejected by server", attrs...)
					return clierr.New(clierr.CodeRuntime, loginErr.UserMessage())
				}

				ctx.Logger.Error("login request failed", "error", err, "username", username)
				return clierr.New(clierr.CodeRuntime, "登录失败：网络异常或服务不可用，请稍后重试")
			}

			expire := "unknown"
			if sess.ExpireTime > 0 {
				expire = time.UnixMilli(sess.ExpireTime).Format(time.RFC3339)
			}

			fmt.Println("登录成功")
			ctx.Logger.Info(
				"登录成功",
				"username", sess.Username,
				"expire_time", sess.ExpireTime,
				"expire_time_rfc3339", expire,
				"token", maskToken(sess.Token),
				"gitlab_id", sess.GitlabID,
				"gitlab_token", maskToken(sess.GitlabToken),
			)
			return nil
		},
	}

	cmd.Flags().StringVarP(&username, "username", "u", "", "login username")
	cmd.Flags().StringVarP(&password, "password", "p", "", "login password (less secure in shell history)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin")

	_ = cmd.MarkFlagRequired("username")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show stored login session",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			sess, err := ctx.SessionStore.Load()
			if err != nil {
				return clierr.Wrap(clierr.CodeRuntime, "load session failed", err)
			}

			expire := "unknown"
			if sess.ExpireTime > 0 {
				expire = time.UnixMilli(sess.ExpireTime).Format(time.RFC3339)
			}

			ctx.Logger.Info(
				"auth status",
				"logged_in", true,
				"username", sess.Username,
				"expire_time", sess.ExpireTime,
				"expire_time_rfc3339", expire,
				"expired", sess.ExpiredAt(time.Now()),
				"token", maskToken(sess.Token),
				"gitlab_id", sess.GitlabID,
				"gitlab_token", maskToken(sess.GitlabToken),
				"session_file", ctx.SessionStore.Path(),
			)
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear local session",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := app.FromContext(cmd.Context())
			if err != nil {
				return err
			}

			if err := ctx.SessionStore.Clear(); err != nil {
				return clierr.Wrap(clierr.CodeRuntime, "logout failed", err)
			}
			ctx.Logger.Info("logout success")
			return nil
		},
	}
}

func readPasswordFromStdin() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		if line == "" {
			return "", err
		}
	}
	return strings.TrimSpace(line), nil
}

func maskToken(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:8] + "..." + token[len(token)-4:]
}
