package app

import (
	"context"
	"log/slog"

	"gomall-cli/internal/clierr"
	"gomall-cli/internal/config"
	"gomall-cli/internal/gomallapi"
	"gomall-cli/internal/session"
)

// Context stores shared runtime dependencies.
type Context struct {
	Config       config.Config
	Logger       *slog.Logger
	APIClient    *gomallapi.Client
	SessionStore *session.Store
}

type contextKey struct{}

func WithContext(ctx context.Context, c *Context) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

func FromContext(ctx context.Context) (*Context, error) {
	c, ok := ctx.Value(contextKey{}).(*Context)
	if !ok || c == nil {
		return nil, clierr.New(clierr.CodeInternal, "app context not initialized")
	}
	return c, nil
}
