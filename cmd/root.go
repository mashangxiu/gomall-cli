package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"gomall-cli/internal/app"
	"gomall-cli/internal/clierr"
	"gomall-cli/internal/config"
	"gomall-cli/internal/gomallapi"
	"gomall-cli/internal/logging"
	"gomall-cli/internal/session"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

// Execute runs the root command.
func Execute() error {
	rootCmd, err := newRootCmd()
	if err != nil {
		return clierr.Wrap(clierr.CodeInternal, "failed to build root command", err)
	}
	return rootCmd.Execute()
}

func newRootCmd() (*cobra.Command, error) {
	var cfgFile string

	rootCmd := &cobra.Command{
		Use:           "gomall-cli",
		Short:         "Enterprise-grade CLI scaffold for gomall",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cmd, cfgFile)
			if err != nil {
				return clierr.Wrap(clierr.CodeConfig, "failed to load config", err)
			}

			logger, err := logging.New(cfg)
			if err != nil {
				return clierr.Wrap(clierr.CodeConfig, "failed to initialize logger", err)
			}

			sessionStore, err := session.NewStore(cfg.Auth.SessionFile)
			if err != nil {
				return clierr.Wrap(clierr.CodeConfig, "failed to initialize session store", err)
			}
			client, err := gomallapi.NewClient(cfg, func(ctx context.Context) (string, error) {
				sess, loadErr := sessionStore.Load()
				if loadErr != nil {
					if errors.Is(loadErr, session.ErrNotFound) {
						return "", fmt.Errorf("not logged in, run \"gomall-cli auth login\" first")
					}
					return "", loadErr
				}
				if sess.ExpiredAt(time.Now()) {
					return "", fmt.Errorf("stored token expired, run \"gomall-cli auth login\" again")
				}
				return sess.Token, nil
			})
			if err != nil {
				return clierr.Wrap(clierr.CodeConfig, "failed to initialize api client", err)
			}

			ctx := &app.Context{
				Config:       cfg,
				Logger:       logger,
				APIClient:    client,
				SessionStore: sessionStore,
			}
			cmd.SetContext(app.WithContext(cmd.Context(), ctx))
			return nil
		},
	}

	if err := bindGlobalFlags(rootCmd, &cfgFile); err != nil {
		return nil, err
	}

	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newHelloCmd())
	rootCmd.AddCommand(newCompletionCmd(rootCmd))
	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newModelCmd())

	return rootCmd, nil
}

func bindGlobalFlags(cmd *cobra.Command, cfgFile *string) error {
	flags := cmd.PersistentFlags()

	flags.StringVar(cfgFile, "config", "", "config file path, e.g. ./gomall-cli.yaml")
	flags.String("env", "dev", "runtime environment: dev/staging/prod")
	flags.String("log-level", "info", "log level: debug/info/warn/error")
	flags.String("log-format", "text", "log format: text/json")
	flags.String("api-base-url", "http://gomall.ac.cn/goMallApi/api", "gomall API base URL")
	flags.Duration("api-timeout", 10_000_000_000, "HTTP timeout, e.g. 5s, 30s")
	flags.Duration("api-lfs-timeout", 30*time.Minute, "LFS download timeout, e.g. 5m, 30m")
	flags.Duration("api-lfs-idle-timeout", 2*time.Minute, "LFS idle timeout with no bytes received, e.g. 30s, 2m")
	flags.Int("api-lfs-chunk-size-mb", 16, "LFS chunk size in MB for multipart range download")
	flags.String("api-lfs-download-url-override", "", "force override LFS download URL base, e.g. http://my.host.cn")
	flags.String("api-lfs-upload-url-override", "", "force override LFS upload URL base, e.g. http://my.host.cn")
	flags.Bool("api-insecure", false, "skip TLS certificate verification")
	flags.String("api-user-agent", "gomall-cli/0.1.0", "custom User-Agent for API requests")
	flags.String("auth-login-path", "/api/auth/login", "login API path")
	flags.String("auth-token-header", "token", "header key carrying auth token")
	flags.String("auth-session-file", "", "session file path, defaults to ~/.gomall-cli/session.json")

	if err := config.BindPFlags(flags); err != nil {
		return fmt.Errorf("bind flags failed: %w", err)
	}
	return nil
}
