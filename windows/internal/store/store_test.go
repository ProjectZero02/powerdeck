package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevicesPersistAndOnlyHashesAreStored(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d, tok, err := s.AddDevice("Phone", "Pixel 9 Pro", now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if strings.Contains(string(raw), tok) {
		t.Fatal("plaintext token written to disk")
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Authenticate(tok, now)
	if !ok || got.ID != d.ID {
		t.Fatal("token should authenticate after reload")
	}
	if _, ok := s2.Authenticate(tok+"x", now); ok {
		t.Fatal("wrong token authenticated")
	}
	if !s2.Settings().AutoUpdate {
		t.Fatal("auto-update should default on")
	}
	if err := s2.RemoveDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Authenticate(tok, now); ok {
		t.Fatal("removed device authenticated")
	}
}

func TestDeviceLimit(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < maxDevices; i++ {
		if _, _, err := s.AddDevice("p", "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddDevice("p", "", time.Now()); err != ErrTooManyDevices {
		t.Fatalf("want ErrTooManyDevices, got %v", err)
	}
}
