package auth

import (
	"testing"
)

func TestHashPassword(t *testing.T) {
	t.Run("returns non-empty bcrypt hash starting with $2", func(t *testing.T) {
		hash, err := HashPassword("hunter2!")
		if err != nil {
			t.Fatalf("HashPassword() error = %v", err)
		}
		if hash == "" {
			t.Fatal("HashPassword() returned empty string")
		}
		if len(hash) < 4 || hash[:2] != "$2" {
			t.Errorf("hash = %q, want to start with $2", hash)
		}
	})

	t.Run("rejects empty password", func(t *testing.T) {
		_, err := HashPassword("")
		if err == nil {
			t.Fatal("HashPassword() expected error for empty password, got nil")
		}
	})

	t.Run("rejects password shorter than 8 bytes", func(t *testing.T) {
		_, err := HashPassword("seven01") // 7 chars
		if err == nil {
			t.Fatal("HashPassword() expected error for <8 char password, got nil")
		}
	})

	t.Run("rejects password longer than 72 bytes", func(t *testing.T) {
		_, err := HashPassword(string(make([]byte, 73)))
		if err == nil {
			t.Fatal("HashPassword() expected error for >72 byte password, got nil")
		}
	})

	t.Run("two HashPassword calls on same plaintext produce different hashes (salting)", func(t *testing.T) {
		hash1, err := HashPassword("hunter2!")
		if err != nil {
			t.Fatalf("HashPassword() error = %v", err)
		}
		hash2, err := HashPassword("hunter2!")
		if err != nil {
			t.Fatalf("HashPassword() error = %v", err)
		}
		if hash1 == hash2 {
			t.Errorf("HashPassword() produced identical hashes (no salting): %q == %q", hash1, hash2)
		}
	})
}

func TestVerifyPassword(t *testing.T) {
	hash, err := HashPassword("hunter2!")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	t.Run("returns nil for correct password", func(t *testing.T) {
		err := VerifyPassword(hash, "hunter2!")
		if err != nil {
			t.Errorf("VerifyPassword() error = %v, want nil", err)
		}
	})

	t.Run("returns ErrInvalidCredentials for wrong password", func(t *testing.T) {
		err := VerifyPassword(hash, "wrong")
		if err != ErrInvalidCredentials {
			t.Errorf("VerifyPassword() error = %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("returns ErrInvalidCredentials for malformed hash", func(t *testing.T) {
		err := VerifyPassword("garbage", "anything")
		if err != ErrInvalidCredentials {
			t.Errorf("VerifyPassword() error = %v, want ErrInvalidCredentials", err)
		}
	})
}