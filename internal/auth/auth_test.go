package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestArgon2PasswordHashing(t *testing.T) {
	password := "CorrectHorseBatteryStaple!2026"

	// Use smaller params for fast test execution
	testParams := &Argon2Params{
		Memory:      16 * 1024,
		Iterations:  1,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}

	hash, err := HashPasswordWithParams(password, testParams)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("expected hash to start with $argon2id$, got %s", hash)
	}

	// Verify correct password
	match, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !match {
		t.Error("expected password to match")
	}

	// Verify wrong password
	match, err = VerifyPassword("WrongPassword123!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword with wrong password returned err: %v", err)
	}
	if match {
		t.Error("expected wrong password not to match")
	}

	// Verify corrupted hash format
	_, err = VerifyPassword(password, "invalid$hash")
	if err == nil {
		t.Error("expected error for corrupted hash, got nil")
	}
}

func TestAES256GCMEncryption(t *testing.T) {
	key := []byte("01234567890123456789012345678901") // exactly 32 bytes
	secret := []byte("maxmind_license_key_secret_xyz123")

	encrypted, err := Encrypt(secret, key)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, secret) {
		t.Errorf("decrypted content mismatch: got %s, want %s", string(decrypted), string(secret))
	}

	// Test invalid key length
	_, err = Encrypt(secret, []byte("short-key"))
	if err == nil {
		t.Error("expected error for short key")
	}

	// Test corrupted ciphertext
	_, err = Decrypt("corrupted-base64-payload", key)
	if err == nil {
		t.Error("expected error for corrupted ciphertext")
	}
}

func TestAPIKeyGeneration(t *testing.T) {
	secret, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	if !strings.HasPrefix(secret, "dm_live_") {
		t.Errorf("expected secret to start with dm_live_, got %s", secret)
	}
	if len(prefix) != 16 {
		t.Errorf("expected 16-char prefix, got %d (%s)", len(prefix), prefix)
	}

	extracted := ExtractPrefix(secret)
	if extracted != prefix {
		t.Errorf("extracted prefix %s does not match %s", extracted, prefix)
	}

	// Verify secret against hash
	match, err := VerifyPassword(secret, hash)
	if err != nil {
		t.Fatalf("VerifyPassword on API key failed: %v", err)
	}
	if !match {
		t.Error("expected API key secret to match hash")
	}
}

func TestSessionAndCSRFTokens(t *testing.T) {
	sess, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken failed: %v", err)
	}
	if len(sess) < 32 {
		t.Errorf("session token too short: %s", sess)
	}

	csrf, err := GenerateCSRFToken()
	if err != nil {
		t.Fatalf("GenerateCSRFToken failed: %v", err)
	}

	if !ValidateCSRFToken(csrf, csrf) {
		t.Error("expected identical CSRF token to validate")
	}
	if ValidateCSRFToken(csrf, "tampered-token") {
		t.Error("expected tampered CSRF token to fail validation")
	}
	if ValidateCSRFToken("", "") {
		t.Error("expected empty CSRF tokens to fail validation")
	}
}
