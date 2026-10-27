package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeysPersistAndOnlyHashesAreStored(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	k, secret, err := s.AddKey(NewKey{Name: "Atlas", Kind: KindAutomation, Scopes: []string{ScopeStatus, ScopeWake}, Devices: []string{"Workstation"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, KeyPrefix) {
		t.Errorf("key %q lacks prefix", secret)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if strings.Contains(string(raw), secret) {
		t.Fatal("plaintext key written to disk")
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Authenticate(secret, now)
	if !ok || got.ID != k.ID || !got.Has(ScopeWake) || got.Has(ScopePower) || !got.MayUse("workstation") || got.MayUse("pve") {
		t.Fatalf("authenticate after reopen: %+v %v", got, ok)
	}
	if _, ok := s2.Authenticate(secret[:len(secret)-1]+"x", now); ok {
		t.Error("wrong key accepted")
	}
	if _, ok := s2.Authenticate("no-prefix", now); ok {
		t.Error("unprefixed key accepted")
	}
	if b, err := s2.BindKey(k.ID, "node-1"); err != nil || b != "node-1" {
		t.Fatal(err)
	}
	if b, _ := s2.BindKey(k.ID, "node-2"); b != "node-1" { // first binding wins
		t.Errorf("rebound to %q", b)
	}
	if got, _ := s2.Authenticate(secret, now); got.BoundNode != "node-1" {
		t.Errorf("bound to %q", got.BoundNode)
	}
	if err := s2.RemoveKey(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Authenticate(secret, now); ok {
		t.Error("removed key still works")
	}
}

func TestKeyLimit(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < maxKeys; i++ {
		if _, _, err := s.AddKey(NewKey{Name: "k", Kind: KindPhone}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddKey(NewKey{Name: "k", Kind: KindPhone}, time.Now()); err != ErrTooManyKeys {
		t.Fatalf("got %v", err)
	}
}

func TestEventsAreCapped(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < maxEvents+5; i++ {
		s.AddEvent(Event{At: time.Now(), Who: "x", Action: "a", Result: string(rune('a' + i%26))})
	}
	ev := s.Events()
	if len(ev) != maxEvents {
		t.Fatalf("%d events", len(ev))
	}
}

func TestPeersKeepKnownMAC(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.PutPeer(Peer{Name: "pc", Address: "100.101.1.10", Port: 47321, Pin: "p", MAC: "aa:bb:cc:dd:ee:ff", LAN: []string{"192.168.1.0/24"}})
	_ = s.PutPeer(Peer{Name: "pc", Address: "100.101.1.10", Port: 47321, Pin: "p"}) // seen again while its status was unavailable
	p, ok := s.Peer("PC")
	if !ok || p.MAC != "aa:bb:cc:dd:ee:ff" || len(p.LAN) != 1 {
		t.Fatalf("%+v %v", p, ok)
	}
}

func TestSchedules(t *testing.T) {
	s, _ := Open(t.TempDir())
	sc, err := s.AddSchedule(Schedule{Action: "shutdown", Time: "01:00", Days: []string{"mon"}, Enabled: true})
	if err != nil || sc.ID == "" {
		t.Fatal(sc, err)
	}
	if len(s.Schedules()) != 1 || s.RemoveSchedule(sc.ID) != nil || len(s.Schedules()) != 0 {
		t.Fatal("schedule round trip")
	}
}

func TestOpenRecoversFromBrokenState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.PutPeer(Peer{Name: "pve", Address: "100.101.1.20", Port: 47321, Pin: "p1"})
	_ = s.PutPeer(Peer{Name: "nas", Address: "100.101.1.21", Port: 47321, Pin: "p2"}) // pve is now in the backup
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"peers": {`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("broken state should fall back: %v", err)
	}
	if _, ok := s2.Peer("pve"); !ok {
		t.Error("backup not used")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json.broken")); err != nil {
		t.Errorf("broken file not kept: %v", err)
	}
	// Both unreadable: start fresh instead of refusing to run.
	_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "state.json.bak"), []byte("x"), 0o600)
	if s3, err := Open(dir); err != nil || !s3.Settings().AutoUpdate {
		t.Errorf("fresh start: %v", err)
	}
}
