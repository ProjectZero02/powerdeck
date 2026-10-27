package nanokvm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" //nolint:gosec // OpenSSL's EVP_BytesToKey, as the NanoKVM web UI uses it
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
)

// passphrase is the NanoKVM firmware's public key for login passwords. It only keeps the password
// out of plain sight on the wire; Tailscale (WireGuard) is what protects it.
const passphrase = "nanokvm-sipeed-2024"

// encryptPassword matches the web UI: encodeURIComponent(CryptoJS.AES.encrypt(password, passphrase)),
// an OpenSSL "Salted__" AES-256-CBC blob with an MD5 EVP_BytesToKey.
func encryptPassword(password string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encryptWithSalt(password, salt), nil
}

func encryptWithSalt(password string, salt []byte) string {
	d := evpBytesToKey([]byte(passphrase), salt)
	block, _ := aes.NewCipher(d[:32])
	pad := aes.BlockSize - len(password)%aes.BlockSize
	plain := append([]byte(password), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, d[32:48]).CryptBlocks(out, plain)
	sealed := append(append([]byte("Salted__"), salt...), out...)
	return url.QueryEscape(base64.StdEncoding.EncodeToString(sealed))
}

// DecryptPassword reverses the login encryption, as the firmware does (used by the fake KVM).
func DecryptPassword(enc string) (string, error) {
	s, err := url.QueryUnescape(enc)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) < 16+aes.BlockSize || string(raw[:8]) != "Salted__" || (len(raw)-16)%aes.BlockSize != 0 {
		return "", errors.New("not an encrypted password")
	}
	d := evpBytesToKey([]byte(passphrase), raw[8:16])
	block, _ := aes.NewCipher(d[:32])
	out := make([]byte, len(raw)-16)
	cipher.NewCBCDecrypter(block, d[32:48]).CryptBlocks(out, raw[16:])
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return "", errors.New("bad padding")
	}
	return string(out[:len(out)-pad]), nil
}

func evpBytesToKey(pass, salt []byte) []byte {
	var out, block []byte
	for len(out) < 48 {
		h := md5.New() //nolint:gosec // see import
		h.Write(block)
		h.Write(pass)
		h.Write(salt)
		block = h.Sum(nil)
		out = append(out, block...)
	}
	return out
}
