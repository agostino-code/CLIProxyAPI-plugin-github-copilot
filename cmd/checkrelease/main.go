// Command checkrelease verifies the complete plugin-store archive set.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var assetPattern = regexp.MustCompile(`^github-copilot_(.+)_(linux|darwin|freebsd)_(amd64|arm64)\.zip$`)

func main() {
	dir := flag.String("dir", "release", "archive directory")
	flag.Parse()
	if err := check(*dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("All five plugin-store ZIPs and SHA-256 checksums verified")
}
func check(dir string) error {
	required := map[string]bool{"linux/amd64": false, "linux/arm64": false, "darwin/amd64": false, "darwin/arm64": false, "freebsd/amd64": false}
	raw, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || len(parts[0]) != 64 || strings.ContainsAny(parts[1], "/\\") {
			return errors.New("invalid checksum line")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return err
		}
		if _, ok := sums[parts[1]]; ok {
			return errors.New("duplicate checksum")
		}
		sums[parts[1]] = parts[0]
	}
	archives, err := filepath.Glob(filepath.Join(dir, "*.zip"))
	if err != nil {
		return err
	}
	version := ""
	for _, path := range archives {
		name := filepath.Base(path)
		match := assetPattern.FindStringSubmatch(name)
		if match == nil {
			return fmt.Errorf("unexpected archive %s", name)
		}
		if version == "" {
			version = match[1]
		} else if version != match[1] {
			return errors.New("mixed archive versions")
		}
		target := match[2] + "/" + match[3]
		seen, known := required[target]
		if !known || seen {
			return fmt.Errorf("unexpected or duplicate target %s", target)
		}
		required[target] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if sums[name] != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("checksum mismatch for %s", name)
		}
		if err := checkZIP(path, match[2]); err != nil {
			return err
		}
	}
	for target, seen := range required {
		if !seen {
			return fmt.Errorf("missing platform %s", target)
		}
	}
	return nil
}
func checkZIP(path, goos string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	library := "github-copilot.so"
	if goos == "darwin" {
		library = "github-copilot.dylib"
	}
	seen := map[string]bool{}
	for _, file := range reader.File {
		if file.Name != library || seen[file.Name] || file.Mode()&os.ModeType != 0 || file.UncompressedSize64 == 0 || file.UncompressedSize64 > 128<<20 {
			return fmt.Errorf("unsafe or unexpected ZIP member %s", file.Name)
		}
		seen[file.Name] = true
	}
	if !seen[library] {
		return errors.New("ZIP must contain one root library")
	}
	return nil
}
