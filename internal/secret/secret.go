// Package secret provides AES-256-GCM encryption for provider credentials at
// rest (PRD §1, ADR-003). The master key never leaves process memory.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/myrouter/myrouter/internal/logx"
)

// Key-derivation parameters (SEC-08). A passphrase master key is stretched with
// PBKDF2-HMAC-SHA256 instead of a single SHA-256 pass, so an attacker who steals
// the database cannot cheaply brute-force the passphrase offline.
const (
	// kdfSaltPrefix is the static, application-wide salt mixed into every
	// passphrase derivation. It is not a secret: its purpose is domain
	// separation, so a MyRouter master key can never collide with the same
	// passphrase used by an unrelated product that also does PBKDF2-SHA256.
	kdfSaltPrefix = "myrouter/master-key/v2"

	// kdfIterations is the PBKDF2 work factor (600k, OWASP 2023+ guidance for
	// PBKDF2-HMAC-SHA256). ~0.6s on a modern laptop, paid once at startup.
	kdfIterations = 600000

	// kdfKeyLen is the AES-256 key length in bytes.
	kdfKeyLen = 32

	// saltEnvVar optionally contributes a deployment-specific salt. When set,
	// the derived key becomes unique to this installation, so a passphrase
	// derived on one host is useless against another host's database. It must
	// stay stable for the lifetime of the data: the salt is never stored next to
	// the ciphertext, so changing (or later setting/unsetting) this variable
	// makes previously stored credentials undecryptable. Set it once, at first
	// boot, and keep it in the same secret store as the master key itself.
	saltEnvVar = "MYROUTER_MASTER_KEY_SALT"

	// keyVersionRaw marks a 32-byte key used verbatim; keyVersionPBKDF2 marks a
	// passphrase stretched with PBKDF2. The value is persisted with each
	// credential so a future rotation can tell the derivations apart.
	keyVersionRaw    = 1
	keyVersionPBKDF2 = 2
)

// Box encrypts and decrypts short secrets.
type Box struct {
	aead cipher.AEAD
	// legacy is the AEAD built from the pre-SEC-08 SHA-256 derivation. It is
	// non-nil only when the master key is a passphrase (or a hex key, which the
	// old code also hashed) and is used purely to read data written before the
	// upgrade. New writes always use aead.
	legacy cipher.AEAD
	ver    int
}

// legacyWarnOnce keeps the "decrypted with legacy KDF" warning to a single log
// line per process instead of one per credential read.
var legacyWarnOnce sync.Once

// New builds a Box from a master key. The key may be a base64- or hex-encoded
// 32 byte value (used verbatim), an arbitrary passphrase (stretched with
// PBKDF2-HMAC-SHA256), or empty (a stored random key file is used by the caller
// instead).
func New(masterKey string) (*Box, error) {
	primary, legacy, ver, err := deriveKeys(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(primary)
	if err != nil {
		return nil, err
	}
	box := &Box{aead: aead, ver: ver}
	if legacy != nil {
		legacyAEAD, err := newAEAD(legacy)
		if err != nil {
			return nil, err
		}
		box.legacy = legacyAEAD
	}
	return box, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// deriveKeys resolves the master key material. It returns the primary (v2) key,
// an optional legacy (v1, single SHA-256) key kept only for backward
// compatibility, and the key version to persist with new ciphertext.
func deriveKeys(masterKey string) (primary, legacy []byte, ver int, err error) {
	masterKey = strings.TrimSpace(masterKey)
	if masterKey == "" {
		return nil, nil, 0, errors.New("master key is empty")
	}
	// A real 32-byte key is used as-is: it already carries full entropy, so
	// stretching it would only add startup latency.
	if raw, ok := decodeRawKey(masterKey); ok {
		// Hex keys were hashed by the pre-SEC-08 code, so keep that hash around
		// as the legacy key; base64 keys were already used verbatim and need no
		// fallback.
		return raw.key, raw.legacy, keyVersionRaw, nil
	}
	salt := kdfSalt()
	primary, err = pbkdf2Cached(masterKey, salt)
	if err != nil {
		return nil, nil, 0, err
	}
	return primary, legacyKey(masterKey), keyVersionPBKDF2, nil
}

// derivedKeyCache memoizes PBKDF2 results. Stretching a passphrase costs ~0.6s,
// and a process may build several Boxes (main server, health monitor, tests);
// the derivation is deterministic, so caching is safe and keeps startup and test
// runs from paying the cost repeatedly. Keys are held in memory only, which is
// already true of the derived key itself.
var derivedKeyCache sync.Map // map[string][]byte

func pbkdf2Cached(passphrase string, salt []byte) ([]byte, error) {
	cacheKey := string(salt) + "\x00" + passphrase
	if v, ok := derivedKeyCache.Load(cacheKey); ok {
		return v.([]byte), nil
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, kdfIterations, kdfKeyLen)
	if err != nil {
		return nil, fmt.Errorf("derive master key: %w", err)
	}
	derivedKeyCache.Store(cacheKey, key)
	return key, nil
}

type rawKey struct {
	key    []byte
	legacy []byte
}

// decodeRawKey reports whether masterKey already is a 32-byte key in one of the
// accepted encodings.
func decodeRawKey(masterKey string) (rawKey, bool) {
	if b, err := base64.StdEncoding.DecodeString(masterKey); err == nil && len(b) == 32 {
		return rawKey{key: b}, true
	}
	if b, err := base64.RawURLEncoding.DecodeString(masterKey); err == nil && len(b) == 32 {
		return rawKey{key: b}, true
	}
	if b, err := hex.DecodeString(masterKey); err == nil && len(b) == 32 {
		return rawKey{key: b, legacy: legacyKey(masterKey)}, true
	}
	return rawKey{}, false
}

// legacyKey reproduces the pre-SEC-08 derivation: one unsalted SHA-256 pass. It
// exists only so credentials written by older builds stay readable.
func legacyKey(masterKey string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(masterKey)))
	return sum[:]
}

// kdfSalt returns the PBKDF2 salt: the fixed application prefix plus, when
// present, the deployment-specific MYROUTER_MASTER_KEY_SALT value. The env var
// is read per Box construction (i.e. once at startup), never per encryption, so
// a running process always uses a consistent salt.
func kdfSalt() []byte {
	salt := kdfSaltPrefix
	if extra := strings.TrimSpace(os.Getenv(saltEnvVar)); extra != "" {
		salt += "|" + extra
	}
	return []byte(salt)
}

// Encrypt returns ciphertext and nonce for plaintext.
func (b *Box) Encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ct := b.aead.Seal(nil, nonce, []byte(plaintext), nil)
	return ct, nonce, nil
}

// Decrypt reverses Encrypt. Ciphertext written by a pre-SEC-08 build (single
// SHA-256 derivation) is still readable through the legacy key; such records are
// re-encrypted with the primary key the next time the credential is saved.
func (b *Box) Decrypt(ciphertext, nonce []byte) (string, error) {
	if len(nonce) != b.aead.NonceSize() {
		return "", errors.New("invalid nonce size")
	}
	pt, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err == nil {
		return string(pt), nil
	}
	if b.legacy != nil {
		if legacyPT, legacyErr := b.legacy.Open(nil, nonce, ciphertext, nil); legacyErr == nil {
			warnLegacyDecrypt()
			return string(legacyPT), nil
		}
	}
	return "", fmt.Errorf("decrypt credential: %w", err)
}

// warnLegacyDecrypt reports, once per process, that data predating the KDF
// upgrade was read. The Box has no store handle, so it cannot re-encrypt in
// place; the operator has to re-save the credential to migrate it.
func warnLegacyDecrypt() {
	legacyWarnOnce.Do(func() {
		logx.Default().Warn("credential decrypted with the legacy SHA-256 master key derivation; "+
			"re-save the affected provider credential to migrate it to PBKDF2",
			logx.F("remediation", "update the provider credential once (PATCH /admin/providers/{id}) or rotate the master key"))
	})
}

// Version reports the key version used for stored credentials.
func (b *Box) Version() int { return b.ver }

// Mask renders a display hint for a credential without leaking it, e.g.
// "sk-****a9f2". Short secrets are fully masked.
func Mask(secret string) string {
	s := strings.TrimSpace(secret)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	head := s[:3]
	tail := s[len(s)-4:]
	return head + "****" + tail
}
