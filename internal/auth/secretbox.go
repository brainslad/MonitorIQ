// Package auth implements two-factor authentication (TOTP), LDAP / Active
// Directory sign-in and single sign-on through OpenID Connect.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Box encrypts secrets stored in the database (TOTP secrets, the LDAP bind
// password, the OIDC client secret) with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
	mac  []byte
}

const boxPrefix = "v1:"

// NewBox derives the encryption key from a secret of any length.
func NewBox(secret []byte) (*Box, error) {
	if len(secret) < 16 {
		return nil, errors.New("the secret key must be at least 16 bytes long")
	}
	k := sha256.Sum256(append([]byte("monitoriq-encryption:"), secret...))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	m := sha256.Sum256(append([]byte("monitoriq-signing:"), secret...))
	return &Box{aead: aead, mac: m[:]}, nil
}

// LoadBox uses MONITORIQ_SECRET_KEY when set. Otherwise it reads (or creates
// on first use) a random key file in dataDir.
func LoadBox(secretKey, dataDir string) (*Box, string, error) {
	if secretKey != "" {
		b, err := NewBox([]byte(secretKey))
		return b, "MONITORIQ_SECRET_KEY", err
	}
	path := filepath.Join(dataDir, "secret.key")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, "", err
		}
		data = []byte(hex.EncodeToString(key) + "\n")
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, "", fmt.Errorf("cannot create %s (set MONITORIQ_SECRET_KEY or MONITORIQ_DATA_DIR): %w", dataDir, err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, "", fmt.Errorf("cannot write %s (set MONITORIQ_SECRET_KEY or MONITORIQ_DATA_DIR): %w", path, err)
		}
	} else if err != nil {
		return nil, "", err
	}
	b, err := NewBox([]byte(strings.TrimSpace(string(data))))
	return b, path, err
}

// Seal encrypts plaintext. The associated data binds the ciphertext to its
// purpose (for example "totp:42") so it cannot be moved elsewhere.
func (b *Box) Seal(plaintext, ad string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(ad))
	return boxPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed, ad string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(sealed, boxPrefix)
	if !ok {
		return "", errors.New("unsupported secret format")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) < b.aead.NonceSize() {
		return "", errors.New("corrupted secret")
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, data[:n], data[n:], []byte(ad))
	if err != nil {
		return "", errors.New("cannot decrypt secret: was MONITORIQ_SECRET_KEY changed?")
	}
	return string(pt), nil
}

// Sign returns an HMAC-SHA256 of data (used for short-lived cookies).
func (b *Box) Sign(data []byte) []byte {
	m := hmac.New(sha256.New, b.mac)
	m.Write(data)
	return m.Sum(nil)
}

// SignedValue encodes data with its signature for use in a cookie.
func (b *Box) SignedValue(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(b.Sign(data))
}

// VerifySigned decodes a value created by SignedValue.
func (b *Box) VerifySigned(v string) ([]byte, bool) {
	d, s, ok := strings.Cut(v, ".")
	if !ok {
		return nil, false
	}
	data, err1 := base64.RawURLEncoding.DecodeString(d)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, b.Sign(data)) {
		return nil, false
	}
	return data, true
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
