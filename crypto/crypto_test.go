package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"sync"
	"testing"
)

// generateTestKey creates a random 32-byte key and returns its base64 encoding.
func generateTestKey() string {
	key := make([]byte, keySize)
	_, _ = rand.Read(key)
	return base64.StdEncoding.EncodeToString(key)
}

// resetPackage resets the package-level state for testing.
func resetPackage() {
	keyOnce = sync.Once{}
	encryptionKey = nil
	encryptionAEAD = nil
}

func TestEncryptDecrypt(t *testing.T) {
	resetPackage()
	os.Setenv(envEncryptionKey, generateTestKey())
	defer os.Unsetenv(envEncryptionKey)
	defer resetPackage()

	plaintext := "sk-abc123secret-api-key-456"

	ciphertext, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if ciphertext == plaintext {
		t.Error("ciphertext should differ from plaintext")
	}

	decrypted, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("Decrypt got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecryptEmptyString(t *testing.T) {
	resetPackage()
	os.Setenv(envEncryptionKey, generateTestKey())
	defer os.Unsetenv(envEncryptionKey)
	defer resetPackage()

	ciphertext, err := Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt empty string failed: %v", err)
	}

	decrypted, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt empty string failed: %v", err)
	}
	if decrypted != "" {
		t.Errorf("Decrypt empty string got %q, want empty", decrypted)
	}
}

func TestEncryptRandomNonce(t *testing.T) {
	resetPackage()
	os.Setenv(envEncryptionKey, generateTestKey())
	defer os.Unsetenv(envEncryptionKey)
	defer resetPackage()

	plaintext := "same-plaintext-value"

	ct1, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt (1) failed: %v", err)
	}
	ct2, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt (2) failed: %v", err)
	}

	if ct1 == ct2 {
		t.Error("two encryptions of the same plaintext should produce different ciphertexts (random nonce)")
	}

	// Both should still decrypt to the same plaintext
	d1, err := Decrypt(ct1)
	if err != nil {
		t.Fatalf("Decrypt (1) failed: %v", err)
	}
	d2, err := Decrypt(ct2)
	if err != nil {
		t.Fatalf("Decrypt (2) failed: %v", err)
	}
	if d1 != plaintext || d2 != plaintext {
		t.Error("decrypted values do not match original plaintext")
	}
}

func TestPassthroughWithoutKey(t *testing.T) {
	resetPackage()
	// Ensure no encryption key is set
	os.Unsetenv(envEncryptionKey)
	defer resetPackage()

	plaintext := "my-api-key-123"

	ciphertext, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt passthrough failed: %v", err)
	}
	if ciphertext != plaintext {
		t.Errorf("passthrough Encrypt got %q, want %q", ciphertext, plaintext)
	}

	decrypted, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt passthrough failed: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("passthrough Decrypt got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptAfterKeyChange(t *testing.T) {
	resetPackage()

	key1 := generateTestKey()
	os.Setenv(envEncryptionKey, key1)

	plaintext := "secret-data"

	ciphertext, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt with key1 failed: %v", err)
	}

	// Now switch to a different key
	resetPackage()
	key2 := generateTestKey()
	os.Setenv(envEncryptionKey, key2)
	defer os.Unsetenv(envEncryptionKey)
	defer resetPackage()

	_, err = Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with a different key, got nil")
	}
	// The error message should mention key change
	t.Logf("expected decryption error: %v", err)
}
