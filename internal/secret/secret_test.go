package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	if got := Mask("sk-abcdefgh1234"); got != "sk-****1234" {
		t.Errorf("Mask = %q", got)
	}
	if got := Mask("short"); got != "****" {
		t.Errorf("short mask = %q", got)
	}
	if got := Mask(""); got != "" {
		t.Errorf("empty mask = %q", got)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	box, err := New("test-master-key")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := "sk-" + strings.Repeat("v", 24)
	ct, nonce, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Ciphertext must not contain the plaintext.
	if string(ct) == plaintext {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := box.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plaintext {
		t.Fatalf("round trip mismatch: %q", got)
	}
}

func TestDecryptWrongNonceFails(t *testing.T) {
	box, _ := New("test-master-key")
	ct, nonce, _ := box.Encrypt("hello")
	bad := make([]byte, len(nonce))
	if _, err := box.Decrypt(ct, bad); err == nil {
		t.Fatal("decryption with zeroed nonce must fail")
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	a, _ := New("passphrase")
	b, _ := New("passphrase")
	ct1, n1, _ := a.Encrypt("x")
	got, err := b.Decrypt(ct1, n1)
	if err != nil || got != "x" {
		t.Fatal("same passphrase must derive the same key")
	}
}

// TestPassphraseKDFIsPBKDF2 pins the derivation to PBKDF2-HMAC-SHA256 with the
// documented salt and work factor, so a refactor cannot silently fall back to
// the old single-SHA-256 behaviour.
func TestPassphraseKDFIsPBKDF2(t *testing.T) {
	primary, legacy, ver, err := deriveKeys("passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if ver != keyVersionPBKDF2 {
		t.Fatalf("version = %d, want %d", ver, keyVersionPBKDF2)
	}
	if kdfIterations != 600000 {
		t.Fatalf("iterations = %d, want 600000", kdfIterations)
	}
	if len(primary) != 32 {
		t.Fatalf("primary key length = %d, want 32", len(primary))
	}
	want := pbkdf2Ref(t, "passphrase", kdfSalt())
	if !bytes.Equal(primary, want) {
		t.Fatal("primary key does not match PBKDF2-HMAC-SHA256 output")
	}
	// The primary key must differ from the legacy single-hash derivation.
	if bytes.Equal(primary, legacy) {
		t.Fatal("primary key must not equal the legacy SHA-256 derivation")
	}
	if !bytes.Equal(legacy, legacyKey("passphrase")) {
		t.Fatal("legacy key must be the single SHA-256 of the passphrase")
	}
}

// TestLegacyCiphertextStillDecrypts proves the compatibility path: a credential
// encrypted by a pre-SEC-08 build (single SHA-256 derivation) is still readable.
func TestLegacyCiphertextStillDecrypts(t *testing.T) {
	const passphrase = "legacy-passphrase"
	plaintext := "sk-" + strings.Repeat("w", 24)

	// Simulate the old build: key = SHA-256(passphrase), AES-256-GCM.
	sum := sha256.Sum256([]byte(passphrase))
	ct, nonce := sealWithKey(t, sum[:], plaintext)

	box, err := New(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("legacy ciphertext must stay readable: %v", err)
	}
	if got != plaintext {
		t.Fatalf("got %q, want %q", got, plaintext)
	}

	// New writes must use the primary (PBKDF2) key, not the legacy one.
	ct2, nonce2, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	legacyAEAD, err := newAEAD(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyAEAD.Open(nil, nonce2, ct2, nil); err == nil {
		t.Fatal("new ciphertext must not be readable with the legacy key")
	}
	if got, err := box.Decrypt(ct2, nonce2); err != nil || got != plaintext {
		t.Fatalf("primary round trip failed: %q %v", got, err)
	}
}

// TestDifferentPassphraseFailsDecrypt ensures a wrong passphrase cannot open
// ciphertext written with another one (neither via primary nor legacy).
func TestDifferentPassphraseFailsDecrypt(t *testing.T) {
	good, err := New("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := good.Encrypt("sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := New("wrong-horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Decrypt(ct, nonce); err == nil {
		t.Fatal("decryption with a different passphrase must fail")
	}
}

// TestRawKeysUsedVerbatim keeps the documented behaviour for real 32-byte keys:
// base64 and hex keys are used as-is (no KDF), and a hex key stays readable via
// its legacy SHA-256 derivation.
func TestRawKeysUsedVerbatim(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)

	box, err := New(b64)
	if err != nil {
		t.Fatal(err)
	}
	if box.ver != keyVersionRaw {
		t.Fatalf("base64 key version = %d, want %d", box.ver, keyVersionRaw)
	}
	if box.legacy != nil {
		t.Fatal("base64 keys were always used verbatim; no legacy key expected")
	}
	ct, nonce, err := box.Encrypt("sk-raw")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := newAEAD(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := direct.Open(nil, nonce, ct, nil); err != nil {
		t.Fatalf("base64 key must be used verbatim: %v", err)
	}

	// Hex key: used verbatim now, but old builds hashed it, so both must work.
	hexKey := hex.EncodeToString(raw)
	hexBox, err := New(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	if hexBox.legacy == nil {
		t.Fatal("hex keys need a legacy fallback for pre-upgrade data")
	}
	legacyCT, legacyNonce := sealWithKey(t, legacyKey(hexKey), "sk-hex-legacy")
	if got, err := hexBox.Decrypt(legacyCT, legacyNonce); err != nil || got != "sk-hex-legacy" {
		t.Fatalf("hex legacy decrypt: %q %v", got, err)
	}
}

// TestSaltEnvChangesDerivedKey documents MYROUTER_MASTER_KEY_SALT: setting it
// yields a different key, so a passphrase alone is not portable across hosts.
func TestSaltEnvChangesDerivedKey(t *testing.T) {
	t.Setenv(saltEnvVar, "")
	withoutSalt, _, _, err := deriveKeys("shared-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(saltEnvVar, "host-specific-salt")
	withSalt, _, _, err := deriveKeys("shared-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(withoutSalt, withSalt) {
		t.Fatal("MYROUTER_MASTER_KEY_SALT must influence the derived key")
	}
}

// pbkdf2Ref recomputes the expected key independently of the production helper,
// so a cache hit cannot mask a broken derivation.
func pbkdf2Ref(t *testing.T, passphrase string, salt []byte) []byte {
	t.Helper()
	direct, err := pbkdf2.Key(sha256.New, passphrase, salt, kdfIterations, kdfKeyLen)
	if err != nil {
		t.Fatal(err)
	}
	return direct
}

// sealWithKey encrypts with an arbitrary AES-256-GCM key, mimicking older
// builds that built their AEAD from a different derivation.
func sealWithKey(t *testing.T, key []byte, plaintext string) ([]byte, []byte) {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	return aead.Seal(nil, nonce, []byte(plaintext), nil), nonce
}
