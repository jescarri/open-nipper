package oidc

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	password := "test-password-123"
	plaintext := []byte(`{"access_token":"abc","token_type":"Bearer"}`)

	enc, err := encrypt(plaintext, password)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	dec, err := decrypt(enc, password)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(dec) != string(plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", dec, plaintext)
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	plaintext := []byte("secret data")

	enc, err := encrypt(plaintext, "correct-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = decrypt(enc, "wrong-password")
	if err == nil {
		t.Fatal("expected error when decrypting with wrong password, got nil")
	}
}

func TestDecryptTooShort(t *testing.T) {
	_, err := decrypt([]byte("short"), "password")
	if err == nil {
		t.Fatal("expected error for short ciphertext, got nil")
	}
}

func TestGetTokenNoFile(t *testing.T) {
	dir := t.TempDir()
	store := NewEncryptedFileTokenStore(dir, "nonexistent", "password")

	tok, err := store.GetToken()
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok != nil {
		t.Fatalf("expected nil token, got %+v", tok)
	}
}

func TestSaveAndGetToken(t *testing.T) {
	dir := t.TempDir()
	store := NewEncryptedFileTokenStore(dir, "myserver", "s3cret")

	expiry := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	original := &StoredToken{
		AccessToken:  "access-123",
		TokenType:    "Bearer",
		RefreshToken: "refresh-456",
		Expiry:       expiry,
	}

	if err := store.SaveToken(original); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Verify file exists on disk.
	expectedPath := filepath.Join(dir, "tokens", "myserver.enc")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected token file at %s: %v", expectedPath, err)
	}

	// Read back (should come from cache).
	tok, err := store.GetToken()
	if err != nil {
		t.Fatalf("GetToken (cached): %v", err)
	}
	assertTokenEqual(t, original, tok)

	// Read with a fresh store (forces disk read).
	store2 := NewEncryptedFileTokenStore(dir, "myserver", "s3cret")
	tok2, err := store2.GetToken()
	if err != nil {
		t.Fatalf("GetToken (disk): %v", err)
	}
	assertTokenEqual(t, original, tok2)
}

func TestSaveTokenCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	// Use a nested base path that does not exist yet.
	basePath := filepath.Join(dir, "deep", "nested")
	store := NewEncryptedFileTokenStore(basePath, "srv", "pw")

	tok := &StoredToken{AccessToken: "a", TokenType: "Bearer"}
	if err := store.SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	expectedPath := filepath.Join(basePath, "tokens", "srv.enc")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected file at %s: %v", expectedPath, err)
	}
}

func TestDeleteToken(t *testing.T) {
	dir := t.TempDir()
	store := NewEncryptedFileTokenStore(dir, "del-test", "pw")

	tok := &StoredToken{AccessToken: "x", TokenType: "Bearer"}
	if err := store.SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	if err := store.DeleteToken(); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	// File should be gone.
	filePath := filepath.Join(dir, "tokens", "del-test.enc")
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, got err: %v", err)
	}

	// GetToken should return nil.
	got, err := store.GetToken()
	if err != nil {
		t.Fatalf("GetToken after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil token after delete, got %+v", got)
	}
}

func TestDeleteTokenNoFile(t *testing.T) {
	dir := t.TempDir()
	store := NewEncryptedFileTokenStore(dir, "missing", "pw")

	// Deleting a non-existent token should not error.
	if err := store.DeleteToken(); err != nil {
		t.Fatalf("DeleteToken on missing file: %v", err)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewEncryptedFileTokenStore(dir, "concurrent", "pw")

	tok := &StoredToken{AccessToken: "init", TokenType: "Bearer"}
	if err := store.SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 200)

	// Concurrent readers.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.GetToken()
			if err != nil {
				errs <- err
				return
			}
			if got == nil {
				errs <- nil // acceptable during delete window
				return
			}
			if got.TokenType != "Bearer" {
				errs <- os.ErrInvalid
			}
		}()
	}

	// Concurrent writers.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			t := &StoredToken{
				AccessToken: "tok",
				TokenType:   "Bearer",
			}
			if err := store.SaveToken(t); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent error: %v", err)
		}
	}
}

func assertTokenEqual(t *testing.T, want, got *StoredToken) {
	t.Helper()
	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.TokenType != want.TokenType {
		t.Errorf("TokenType: got %q, want %q", got.TokenType, want.TokenType)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry: got %v, want %v", got.Expiry, want.Expiry)
	}
}
