package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var globalViper = viper.New()

// Config is the global runtime configuration.
type Config struct {
	Env  string
	Log  LogConfig
	API  APIConfig
	Auth AuthConfig
}

type LogConfig struct {
	Level  string
	Format string
}

type APIConfig struct {
	BaseURL        string
	Timeout        time.Duration
	LFSTimeout     time.Duration
	LFSIdleTimeout time.Duration
	Insecure       bool
	UserAgent      string
}

type AuthConfig struct {
	LoginPath   string
	TokenHeader string
	SessionFile string
}

func init() {
	globalViper.SetEnvPrefix("GOMALL")
	globalViper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	globalViper.AutomaticEnv()

	globalViper.SetDefault("env", "dev")
	globalViper.SetDefault("log-level", "info")
	globalViper.SetDefault("log-format", "text")

	globalViper.SetDefault("api-base-url", "http://gomall.ac.cn/goMallApi/api")
	globalViper.SetDefault("api-timeout", "10s")
	globalViper.SetDefault("api-lfs-timeout", "30m")
	globalViper.SetDefault("api-lfs-idle-timeout", "2m")
	globalViper.SetDefault("api-insecure", false)
	globalViper.SetDefault("api-user-agent", "gomall-cli/0.1.0")

	globalViper.SetDefault("auth-login-path", "/api/auth/login")
	globalViper.SetDefault("auth-token-header", "token")
	globalViper.SetDefault("auth-session-file", "")
}

func BindPFlags(flags *pflag.FlagSet) error {
	keys := []string{
		"env",
		"log-level",
		"log-format",
		"api-base-url",
		"api-timeout",
		"api-lfs-timeout",
		"api-lfs-idle-timeout",
		"api-insecure",
		"api-user-agent",
		"auth-login-path",
		"auth-token-header",
		"auth-session-file",
	}
	for _, key := range keys {
		if err := globalViper.BindPFlag(key, flags.Lookup(key)); err != nil {
			return fmt.Errorf("bind flag %s: %w", key, err)
		}
	}
	return nil
}

// Load merges defaults, env vars, config file and command flags.
func Load(cmd *cobra.Command, cfgFile string) (Config, error) {
	if cmd == nil {
		return Config{}, errors.New("nil command")
	}

	if cfgFile != "" {
		globalViper.SetConfigFile(cfgFile)
		if err := globalViper.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config file %s: %w", cfgFile, err)
		}
	} else {
		home, _ := os.UserHomeDir()
		globalViper.SetConfigName("gomall-cli")
		globalViper.SetConfigType("yaml")
		globalViper.AddConfigPath(".")
		if home != "" {
			globalViper.AddConfigPath(filepath.Join(home, ".gomall-cli"))
		}
		if err := globalViper.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return Config{}, fmt.Errorf("read config: %w", err)
			}
		}
	}

	sessionFile := strings.TrimSpace(globalViper.GetString("auth-session-file"))
	if sessionFile == "" {
		sessionFile = defaultSessionFile()
	}

	cfg := Config{
		Env: globalViper.GetString("env"),
		Log: LogConfig{
			Level:  globalViper.GetString("log-level"),
			Format: globalViper.GetString("log-format"),
		},
		API: APIConfig{
			BaseURL:        globalViper.GetString("api-base-url"),
			Timeout:        globalViper.GetDuration("api-timeout"),
			LFSTimeout:     globalViper.GetDuration("api-lfs-timeout"),
			LFSIdleTimeout: globalViper.GetDuration("api-lfs-idle-timeout"),
			Insecure:       globalViper.GetBool("api-insecure"),
			UserAgent:      globalViper.GetString("api-user-agent"),
		},
		Auth: AuthConfig{
			LoginPath:   globalViper.GetString("auth-login-path"),
			TokenHeader: globalViper.GetString("auth-token-header"),
			SessionFile: sessionFile,
		},
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log-level %q", cfg.Log.Level)
	}

	switch cfg.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("invalid log-format %q", cfg.Log.Format)
	}

	if strings.TrimSpace(cfg.API.UserAgent) == "" {
		return fmt.Errorf("api-user-agent cannot be empty")
	}

	u, err := url.Parse(cfg.API.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid api-base-url %q: %w", cfg.API.BaseURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("api-base-url must include scheme and host, got %q", cfg.API.BaseURL)
	}

	if cfg.API.Timeout <= 0 {
		return fmt.Errorf("api-timeout must be > 0")
	}
	if cfg.API.LFSTimeout <= 0 {
		return fmt.Errorf("api-lfs-timeout must be > 0")
	}
	if cfg.API.LFSIdleTimeout <= 0 {
		return fmt.Errorf("api-lfs-idle-timeout must be > 0")
	}

	if strings.TrimSpace(cfg.Auth.LoginPath) == "" {
		return fmt.Errorf("auth-login-path cannot be empty")
	}
	if strings.TrimSpace(cfg.Auth.TokenHeader) == "" {
		return fmt.Errorf("auth-token-header cannot be empty")
	}
	if strings.TrimSpace(cfg.Auth.SessionFile) == "" {
		return fmt.Errorf("auth-session-file cannot be empty")
	}
	return nil
}

func defaultSessionFile() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "./.gomall-cli/session.json"
	}
	return filepath.Join(home, ".gomall-cli", "session.json")
}
