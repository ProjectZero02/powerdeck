//go:build windows

package secret

import (
	"encoding/base64"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Default is DPAPI in the service account's scope.
func Default() Box { return dpapi{} }

// Dir is "" on Windows: DPAPI keeps no key file.
func Dir() string { return "" }

type dpapi struct{}

const dpapiPrefix = "d1:"

var entropy = []byte("PowerDeck KVM credentials")

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func take(out *windows.DataBlob) []byte {
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

func (dpapi) Seal(plain string) (string, error) {
	var out windows.DataBlob
	if err := windows.CryptProtectData(blob([]byte(plain)), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	return dpapiPrefix + base64.RawStdEncoding.EncodeToString(take(&out)), nil
}

func (dpapi) Open(sealed string) (string, error) {
	rest, ok := strings.CutPrefix(sealed, dpapiPrefix)
	if !ok {
		return "", ErrTampered
	}
	raw, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil || len(raw) == 0 {
		return "", ErrTampered
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(raw), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", ErrTampered
	}
	return string(take(&out)), nil
}
