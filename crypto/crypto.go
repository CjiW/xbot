package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	log "xbot/logger"
)

var (
	//nolint:unused // used by tests (resetPackage) and reserved for key rotation
	encryptionKey  []byte
	encryptionAEAD cipher.AEAD
	keyOnce        sync.Once
)

const (
	envEncryptionKey = "XBOT_ENCRYPTION_KEY"
	nonceSize        = 12
	keySize          = 32
)

// Init loads the encryption key from the XBOT_ENCRYPTION_KEY environment variable.
// It is safe to call multiple times; subsequent calls after the first are no-ops.
func Init() {
	keyOnce.Do(loadKey)
}

// loadKey reads and validates the encryption key from the environment.
func loadKey() {
	encoded := os.Getenv(envEncryptionKey)
	if encoded == "" {
		log.Warn("XBOT_ENCRYPTION_KEY is not set; API keys will be stored in plaintext. " +
			"Set a base64-encoded 32-byte key for encryption.")
		return
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		log.WithError(err).Error("XBOT_ENCRYPTION_KEY is not valid base64; API keys will be stored in plaintext")
		return
	}
	if len(key) != keySize {
		log.WithField("actual_length", len(key)).Error(
			fmt.Sprintf("XBOT_ENCRYPTION_KEY must decode to exactly %d bytes; API keys will be stored in plaintext", keySize))
		return
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		log.WithError(err).Error("failed to create AES cipher; API keys will be stored in plaintext")
		return
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		log.WithError(err).Error("failed to create GCM; API keys will be stored in plaintext")
		return
	}

	encryptionKey = key
	encryptionAEAD = aead
	log.Info("encryption key loaded successfully")
}

// isReady returns true if the encryption key has been loaded and is usable.
func isReady() bool {
	Init()
	return encryptionAEAD != nil
}

// Encrypt encrypts the given plaintext using AES-256-GCM.
// The ciphertext is returned as base64(nonce + encrypted).
// If no encryption key is configured, the plaintext is returned as-is (passthrough).
func Encrypt(plaintext string) (string, error) {
	if !isReady() {
		return plaintext, nil
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := encryptionAEAD.Seal(nil, nonce, []byte(plaintext), nil)

	// Prepend nonce to ciphertext, then base64 encode
	combined := make([]byte, nonceSize+len(ciphertext))
	copy(combined[:nonceSize], nonce)
	copy(combined[nonceSize:], ciphertext)

	return base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt decrypts the given base64-encoded ciphertext.
// The input must be in the format base64(nonce + ciphertext).
// If no encryption key is configured, the input is returned as-is (passthrough).
func Decrypt(ciphertext string) (string, error) {
	if !isReady() {
		return ciphertext, nil
	}

	combined, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		// If it's not valid base64, it might be a legacy plaintext value.
		log.WithError(err).Warn("failed to base64-decode ciphertext; returning raw value (may be legacy plaintext)")
		return ciphertext, nil
	}

	if len(combined) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce := combined[:nonceSize]
	encrypted := combined[nonceSize:]

	plaintext, err := encryptionAEAD.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed (encryption key may have changed): %w", err)
	}

	return string(plaintext), nil
}
