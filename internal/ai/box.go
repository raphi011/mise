package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// KeyFile is the name of the generated secret key in the data directory.
const KeyFile = "secret.key"

// keySize is the AES-256 key length.
const keySize = 32

// boxVersion prefixes every sealed value, so the format can change later.
const boxVersion = "v1:"

// ErrUnreadable means a sealed value can't be opened: it was sealed with
// another key (a restore without secret.key) or for another household.
var ErrUnreadable = errors.New("ai: stored secret can't be read with this server's key")

// Box seals secrets such as API keys with AES-256-GCM. The household ID is
// the additional data, so a value copied to another household's row does
// not open.
type Box struct{ aead cipher.AEAD }

// NewBox returns a Box for a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("ai: secret key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("ai: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("ai: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext for the household.
func (b *Box) Seal(householdID uuid.UUID, plaintext string) string {
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce) // never fails, see crypto/rand
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), householdID[:])
	return boxVersion + base64.StdEncoding.EncodeToString(out)
}

// Open decrypts a value sealed for the household, or fails with
// ErrUnreadable.
func (b *Box) Open(householdID uuid.UUID, sealed string) (string, error) {
	enc, ok := strings.CutPrefix(sealed, boxVersion)
	if !ok {
		return "", ErrUnreadable
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	n := b.aead.NonceSize()
	if err != nil || len(raw) < n {
		return "", ErrUnreadable
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], householdID[:])
	if err != nil {
		return "", ErrUnreadable
	}
	return string(plain), nil
}

// LoadKey returns the secret key: encoded (MISE_SECRET_KEY, base64) when
// set, else the key in dataDir/secret.key, generated on first use.
func LoadKey(encoded, dataDir string) ([]byte, error) {
	if encoded != "" {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(key) != keySize {
			return nil, fmt.Errorf("ai: MISE_SECRET_KEY must be %d bytes, base64-encoded", keySize)
		}
		return key, nil
	}
	path := filepath.Join(dataDir, KeyFile)
	key, err := readKeyFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("ai: %w", err)
	}
	key = make([]byte, keySize)
	_, _ = rand.Read(key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return readKeyFile(path) // another process created it first
	}
	if err != nil {
		return nil, fmt.Errorf("ai: create %s: %w", path, err)
	}
	_, err = f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("ai: write %s: %w", path, err)
	}
	return key, nil
}

func readKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("ai: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != keySize {
		return nil, fmt.Errorf("ai: %s is not a base64-encoded %d-byte key", path, keySize)
	}
	return key, nil
}
