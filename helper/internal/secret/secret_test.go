package secret

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeyFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	k := &KeyFile{Path: filepath.Join(dir, "secret.key")}
	s, err := k.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "hunter2") {
		t.Fatal("not sealed")
	}
	if s2, _ := k.Seal("hunter2"); s2 == s {
		t.Error("same nonce twice")
	}
	// A fresh process (same key file) opens it.
	if p, err := (&KeyFile{Path: k.Path}).Open(s); err != nil || p != "hunter2" {
		t.Fatalf("open: %q %v", p, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(k.Path)
		di, _ := os.Stat(dir)
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("modes %v %v", fi.Mode(), di.Mode())
		}
	}
	// Tampered, or another installation's key.
	b := []byte(s)
	b[len(b)-2] ^= 1
	if _, err := k.Open(string(b)); !errors.Is(err, ErrTampered) {
		t.Errorf("tampered: %v", err)
	}
	other := &KeyFile{Path: filepath.Join(t.TempDir(), "secret.key")}
	if _, err := other.Open(s); !errors.Is(err, ErrTampered) {
		t.Errorf("no key: %v", err)
	}
	_, _ = other.Seal("x")
	if _, err := other.Open(s); !errors.Is(err, ErrTampered) {
		t.Errorf("other key: %v", err)
	}
	if _, err := k.Open("plain text"); !errors.Is(err, ErrTampered) {
		t.Errorf("unsealed: %v", err)
	}
}

func TestEmptyKeyFileReplaced(t *testing.T) {
	// An older version crashing between creating the file and writing the key left it empty.
	k := &KeyFile{Path: filepath.Join(t.TempDir(), "secret.key")}
	if err := os.WriteFile(k.Path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(k.Path); len(b) != 32 {
		t.Errorf("key file %d bytes", len(b))
	}
	if p, err := (&KeyFile{Path: k.Path}).Open(s); err != nil || p != "hunter2" {
		t.Fatalf("open: %q %v", p, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(k.Path), ".secret-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestDefaultBox(t *testing.T) {
	// On Windows this is DPAPI (the Windows CI job runs it).
	t.Setenv("POWERDECK_KEYS", t.TempDir())
	b := Default()
	s, err := b.Seal("pw")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := Default().Open(s); err != nil || p != "pw" {
		t.Fatalf("%q %v", p, err)
	}
}
