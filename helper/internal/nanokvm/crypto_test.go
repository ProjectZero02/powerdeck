package nanokvm

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

// Expected ciphertexts from: openssl enc -aes-256-cbc -md md5 -S <salt> -pass pass:nanokvm-sipeed-2024 -a
// (the same vectors as the app's NanoKvmCryptoTest).
func TestEncryptMatchesOpenSSL(t *testing.T) {
	for _, v := range []struct{ plain, salt, ct string }{
		{"Tr0ub4dor&3 +/=?% pässwörd", "0102030405060708", "CdZkOzG0IEdxjqegpohcXAKLO9Uk5MAGDzuqnDy8mLQ="},
		{"admin", "a1b2c3d4e5f60718", "0dKkPa6381Bd25DXo2I8vQ=="},
	} {
		salt, _ := hex.DecodeString(v.salt)
		enc := encryptWithSalt(v.plain, salt)
		if strings.ContainsAny(enc, "+/=") {
			t.Errorf("not URI-encoded: %s", enc)
		}
		s, _ := url.QueryUnescape(enc)
		raw, _ := base64.StdEncoding.DecodeString(s)
		if string(raw[:8]) != "Salted__" || hex.EncodeToString(raw[8:16]) != v.salt || base64.StdEncoding.EncodeToString(raw[16:]) != v.ct {
			t.Errorf("%q: got %s", v.plain, s)
		}
		if back, err := DecryptPassword(enc); err != nil || back != v.plain {
			t.Errorf("round trip: %q %v", back, err)
		}
	}
}
