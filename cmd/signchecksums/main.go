package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	input := flag.String("input", "", "checksum manifest to sign")
	output := flag.String("output", "", "detached signature path")
	flag.Parse()
	if *input == "" || *output == "" {
		fatal("--input and --output are required")
	}
	encoded := strings.TrimSpace(os.Getenv("TALENTO_RELEASE_PRIVATE_KEY"))
	if encoded == "" {
		fatal("TALENTO_RELEASE_PRIVATE_KEY is required")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		fatal("decode TALENTO_RELEASE_PRIVATE_KEY: %v", err)
	}
	var privateKey ed25519.PrivateKey
	switch len(raw) {
	case ed25519.SeedSize:
		privateKey = ed25519.NewKeyFromSeed(raw)
	case ed25519.PrivateKeySize:
		privateKey = ed25519.PrivateKey(raw)
	default:
		fatal("TALENTO_RELEASE_PRIVATE_KEY must contain a 32-byte seed or 64-byte private key")
	}
	if expected := strings.TrimSpace(os.Getenv("TALENTO_RELEASE_PUBLIC_KEY")); expected != "" {
		actual := base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))
		if actual != expected {
			fatal("release signing key does not match TALENTO_RELEASE_PUBLIC_KEY")
		}
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		fatal("read input: %v", err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data)) + "\n"
	// #nosec G703 -- the release operator explicitly selects the detached-signature output path.
	if err := os.WriteFile(*output, []byte(signature), 0o644); err != nil {
		fatal("write signature: %v", err)
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "signchecksums: "+format+"\n", args...)
	os.Exit(1)
}
