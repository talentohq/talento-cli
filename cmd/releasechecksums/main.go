package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	directory := flag.String("dir", "release-dist", "directory containing release assets")
	output := flag.String("output", "checksums.txt", "checksum manifest path")
	flag.Parse()
	entries, err := os.ReadDir(*directory)
	check(err)
	lines := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), "checksums.txt") {
			continue
		}
		file, err := os.Open(filepath.Join(*directory, entry.Name()))
		check(err)
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		check(copyErr)
		check(closeErr)
		lines = append(lines, fmt.Sprintf("%s  %s", hex.EncodeToString(hash.Sum(nil)), entry.Name()))
	}
	sort.Strings(lines)
	check(os.WriteFile(*output, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func check(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
