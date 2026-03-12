package oidc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// StoredToken represents an OAuth token persisted to disk.
type StoredToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// EncryptedFileTokenStore persists OAuth tokens encrypted at rest using
// AES-256-GCM with an Argon2id-derived key.
type EncryptedFileTokenStore struct {
	mu       sync.RWMutex
	filePath string
	password string
	cached   *StoredToken
}

// NewEncryptedFileTokenStore creates a new token store that writes encrypted
// token files under {basePath}/tokens/{serverName}.enc.
func NewEncryptedFileTokenStore(basePath, serverName, password string) *EncryptedFileTokenStore {
	return &EncryptedFileTokenStore{
		filePath: filepath.Join(basePath, "tokens", serverName+".enc"),
		password: password,
	}
}

// GetToken returns the stored token, reading from cache first and falling back
// to disk. Returns (nil, nil) when no token file exists.
func (s *EncryptedFileTokenStore) GetToken() (*StoredToken, error) {
	s.mu.RLock()
	if s.cached != nil {
		tok := *s.cached
		s.mu.RUnlock()
		return &tok, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock.
	if s.cached != nil {
		tok := *s.cached
		return &tok, nil
	}

	raw, err := os.ReadFile(s.filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading token file: %w", err)
	}

	plain, err := decrypt(raw, s.password)
	if err != nil {
		return nil, fmt.Errorf("decrypting token: %w", err)
	}

	var tok StoredToken
	if err := json.Unmarshal(plain, &tok); err != nil {
		return nil, fmt.Errorf("unmarshalling token: %w", err)
	}

	s.cached = &tok
	out := tok
	return &out, nil
}

// SaveToken encrypts and persists the token to disk, creating intermediate
// directories as needed, and updates the in-memory cache.
func (s *EncryptedFileTokenStore) SaveToken(token *StoredToken) error {
	plain, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshalling token: %w", err)
	}

	enc, err := encrypt(plain, s.password)
	if err != nil {
		return fmt.Errorf("encrypting token: %w", err)
	}

	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}

	if err := os.WriteFile(s.filePath, enc, 0o600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *token
	s.cached = &copied
	return nil
}

// DeleteToken removes the encrypted token file from disk and clears the cache.
func (s *EncryptedFileTokenStore) DeleteToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cached = nil

	if err := os.Remove(s.filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing token file: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Encryption helpers
// ---------------------------------------------------------------------------

const (
	saltLen  = 16
	nonceLen = 12
)

// deriveKey uses Argon2id to derive a 32-byte key from a password and salt.
func deriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
}

// encrypt encrypts data using AES-256-GCM with a key derived via Argon2id.
// Output format: salt(16) || nonce(12) || ciphertext+tag.
func encrypt(data []byte, password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}

	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	key := deriveKey(password, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, data, nil)

	out := make([]byte, 0, saltLen+nonceLen+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// decrypt reverses the encrypt operation. It expects the format:
// salt(16) || nonce(12) || ciphertext+tag.
func decrypt(data []byte, password string) ([]byte, error) {
	if len(data) < saltLen+nonceLen {
		return nil, errors.New("ciphertext too short")
	}

	salt := data[:saltLen]
	nonce := data[saltLen : saltLen+nonceLen]
	ciphertext := data[saltLen+nonceLen:]

	key := deriveKey(password, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}

	return plain, nil
}
