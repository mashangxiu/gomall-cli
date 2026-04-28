package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"gomall-cli/internal/config"
	"gomall-cli/internal/gomallapi"
	"gomall-cli/internal/session"
)

type Service struct {
	client *gomallapi.Client
	store  *session.Store
	cfg    config.AuthConfig
}

func NewService(client *gomallapi.Client, store *session.Store, cfg config.AuthConfig) *Service {
	return &Service{client: client, store: store, cfg: cfg}
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginRespData struct {
	Token      string `json:"token"`
	ExpireTime int64  `json:"expireTime"`
	Username   string `json:"username"`
}

// LoginFailureError describes business-level login rejection returned by server.
type LoginFailureError struct {
	Code      int
	Message   string
	RequestID string
}

func (e *LoginFailureError) Error() string {
	if strings.TrimSpace(e.RequestID) == "" {
		return fmt.Sprintf("code=%d message=%s", e.Code, e.Message)
	}
	return fmt.Sprintf("code=%d message=%s request_id=%s", e.Code, e.Message, e.RequestID)
}

func (e *LoginFailureError) UserMessage() string {
	if strings.TrimSpace(e.Message) != "" {
		return "登录失败：" + e.Message
	}
	return "登录失败：请稍后重试"
}

func (s *Service) Login(ctx context.Context, username, password string) (session.Session, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return session.Session{}, fmt.Errorf("username cannot be empty")
	}
	if password == "" {
		return session.Session{}, fmt.Errorf("password cannot be empty")
	}

	env, err := s.client.Do(ctx, http.MethodPost, s.cfg.LoginPath, loginReq{
		Username: username,
		Password: password,
	}, false)
	if err != nil {
		return session.Session{}, err
	}

	if env.Code != 200 {
		return session.Session{}, &LoginFailureError{
			Code:      env.Code,
			Message:   env.Message,
			RequestID: env.EffectiveRequestID(),
		}
	}

	var data loginRespData
	if err := env.DecodeData(&data); err != nil {
		return session.Session{}, err
	}

	if data.Token == "" {
		return session.Session{}, fmt.Errorf("login response missing token")
	}
	if strings.TrimSpace(data.Username) == "" {
		data.Username = username
	}

	sess := session.Session{
		Token:      data.Token,
		ExpireTime: data.ExpireTime,
		Username:   data.Username,
	}

	if err := s.store.Save(sess); err != nil {
		return session.Session{}, fmt.Errorf("save session: %w", err)
	}
	return sess, nil
}
