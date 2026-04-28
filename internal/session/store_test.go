package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreSaveLoadClear(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	in := Session{
		Token:       "abc-sensitive-token",
		ExpireTime:  time.Now().Add(time.Hour).UnixMilli(),
		Username:    "gomall",
		GitlabToken: "glpat-test-token",
		GitlabID:    93,
	}
	if err := store.Save(in); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(raw), in.Token) {
		t.Fatalf("session file should not contain plaintext token")
	}

	out, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if out.Token != in.Token || out.Username != in.Username || out.ExpireTime != in.ExpireTime || out.GitlabToken != in.GitlabToken || out.GitlabID != in.GitlabID {
		t.Fatalf("Load() = %+v, want %+v", out, in)
	}
	if out.SavedAt == 0 {
		t.Fatalf("Load().SavedAt should be set")
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	_, err = store.Load()
	if err == nil {
		t.Fatalf("Load() should fail after clear")
	}
}

func TestStoreLoadsLegacyPlaintext(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "session.json")
	legacy := `{"token":"plain-token","expireTime":123,"username":"old-user"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if sess.Token != "plain-token" || sess.Username != "old-user" || sess.ExpireTime != 123 {
		t.Fatalf("Load() unexpected result: %+v", sess)
	}
}
