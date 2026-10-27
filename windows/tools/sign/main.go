// sign writes an Ed25519 signature for a release installer, for PowerDeck's in-app updater.
//
//	POWERDECK_UPDATE_SIGNING_KEY=<base64 seed> go run ./tools/sign dist/PowerDeck-Setup.exe
//
// writes dist/PowerDeck-Setup.exe.sig (base64) and checks it against the public key built into the app.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/projectzero02/powerdeck/windows/internal/update"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sign FILE")
		os.Exit(2)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("POWERDECK_UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "sign: POWERDECK_UPDATE_SIGNING_KEY must be a base64 Ed25519 seed")
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), data))
	// Refuse to publish a signature the shipped app wouldn't accept.
	if err := update.Verify(data, []byte(sig)); err != nil {
		fmt.Fprintln(os.Stderr, "sign: key does not match the app's public key:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1]+".sig", []byte(sig+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	fmt.Println("signed", os.Args[1])
}
