package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrNotFound = errors.New("session not found")

const envelopeAlg = "AES-256-GCM"

// Session stores auth material used by authenticated API commands.
type Session struct {
	Token      string `json:"token"`
	ExpireTime int64  `json:"expireTime"`
	Username   string `json:"username"`
	SavedAt    int64  `json:"savedAt"`
}

func (s Session) ExpiredAt(now time.Time) bool {
	if s.ExpireTime <= 0 {
		return false
	}
	return now.UnixMilli() >= s.ExpireTime
}

// Store persists session data as local json file.
type Store struct {
	path string
	aead cipher.AEAD
}

type encryptedEnvelope struct {
	V          int    `json:"v"`
	Alg        string `json:"alg"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func NewStore(path string) (*Store, error) {
	aead, err := newAEAD()
	if err != nil {
		return nil, err
	}
	return &Store{path: path, aead: aead}, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Save(session Session) error {
	if session.Token == "" {
		return fmt.Errorf("token cannot be empty")
	}
	if session.Username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	session.SavedAt = time.Now().UnixMilli()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	plain, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	env, err := s.encrypt(plain)
	if err != nil {
		return err
	}

	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal encrypted envelope: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp session file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	return nil
}

func (s *Store) Load() (Session, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("read session file: %w", err)
	}

	plain, err := s.decryptIfNeeded(b)
	if err != nil {
		return Session{}, err
	}

	var session Session
	if err := json.Unmarshal(plain, &session); err != nil {
		return Session{}, fmt.Errorf("unmarshal session: %w", err)
	}

	if session.Token == "" || session.Username == "" {
		return Session{}, fmt.Errorf("invalid session file: missing token or username")
	}
	return session, nil
}

func (s *Store) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session file: %w", err)
	}
	return nil
}

func newAEAD() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(effectiveSecret()))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("init aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return aead, nil
}

func effectiveSecret() string {
	// Key is bound to this machine/user. If session file is copied elsewhere,
	// derived key changes and decryption fails.
	parts := []string{"gomall-cli"}
	if machineID := machineIdentity(); machineID != "" {
		parts = append(parts, "machine_id="+machineID)
	}
	if userID := userIdentity(); userID != "" {
		parts = append(parts, "user="+userID)
	}
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	parts = append(parts, "host="+host, "home="+home)
	return strings.Join(parts, "|")
}

func (s *Store) encrypt(plain []byte) (encryptedEnvelope, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedEnvelope{}, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := s.aead.Seal(nil, nonce, plain, nil)

	return encryptedEnvelope{
		V:          1,
		Alg:        envelopeAlg,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func (s *Store) decryptIfNeeded(raw []byte) ([]byte, error) {
	var env encryptedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Backward compatibility: legacy plaintext session json.
		return raw, nil
	}
	if env.Alg != envelopeAlg || env.Ciphertext == "" || env.Nonce == "" {
		// Not an encrypted envelope, treat as plaintext.
		return raw, nil
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	plain, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt session: %w", err)
	}
	return plain, nil
}
