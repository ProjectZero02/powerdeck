// sign writes Ed25519 signatures for release downloads (the Windows installer, the Linux helper),
// checked by the helper's updater and the Linux install script.
//
//	POWERDECK_UPDATE_SIGNING_KEY=<base64 seed> go run ./tools/sign FILE...
//
// writes FILE.sig (base64) for each and checks it against the public key built into the helper.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/projectzero02/powerdeck/helper/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sign FILE...")
		os.Exit(2)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("POWERDECK_UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "sign: POWERDECK_UPDATE_SIGNING_KEY must be a base64 Ed25519 seed")
		os.Exit(1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sign:", err)
			os.Exit(1)
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
		// Refuse to publish a signature the shipped helper wouldn't accept.
		if err := update.Verify(data, []byte(sig)); err != nil {
			fmt.Fprintln(os.Stderr, "sign: key does not match the helper's public key:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(path+".sig", []byte(sig+"\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sign:", err)
			os.Exit(1)
		}
		fmt.Println("signed", path)
	}
}
