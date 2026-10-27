// Package secret seals small secrets (KVM passwords) before they're written to the state file, so a
// copy of the data folder or a backup of it doesn't give them away.
//
// Windows: DPAPI in the service account's scope (only SYSTEM can open them). Elsewhere: AES-256-GCM
// with a random 32-byte key in its own root-only file, kept apart from the data folder
// (/etc/powerdeck/secret.key on Linux). Neither protects against root or an administrator on the
// machine itself, who can read the key or run code as the service.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Box seals and opens secrets.
type Box interface {
	Seal(plain string) (string, error)
	Open(sealed string) (string, error)
}

// ErrTampered means a sealed value can't be opened: changed, or sealed with another key.
var ErrTampered = errors.New("the stored secret can't be opened (changed, or from another installation)")

// KeyFile seals with AES-256-GCM under a key kept in Path, created on first use (0600, folder 0700).
type KeyFile struct {
	Path string

	mu  sync.Mutex
	key []byte
}

const keyPrefix = "k1:"

func (k *KeyFile) load(create bool) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != nil {
		return k.key, nil
	}
	b, err := os.ReadFile(k.Path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s isn't a PowerDeck key", k.Path)
		}
		k.key = b
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: two writers can't both create it with different keys.
	f, err := os.OpenFile(k.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) { // created by someone else meanwhile: use theirs
		if b, err = os.ReadFile(k.Path); err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("%s isn't a PowerDeck key", k.Path)
		}
		k.key = b
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	k.key = b
	return b, nil
}

func (k *KeyFile) aead(create bool) (cipher.AEAD, error) {
	key, err := k.load(create)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plain.
func (k *KeyFile) Seal(plain string) (string, error) {
	g, err := k.aead(true)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return keyPrefix + base64.RawStdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts a value from Seal.
func (k *KeyFile) Open(sealed string) (string, error) {
	rest, ok := strings.CutPrefix(sealed, keyPrefix)
	if !ok {
		return "", ErrTampered
	}
	raw, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil {
		return "", ErrTampered
	}
	g, err := k.aead(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrTampered
		}
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", ErrTampered
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return "", ErrTampered
	}
	return string(plain), nil
}
