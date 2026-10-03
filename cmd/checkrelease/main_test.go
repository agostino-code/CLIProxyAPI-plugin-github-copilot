package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectIncompleteRelease(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if check(dir) == nil {
		t.Fatal("incomplete release accepted")
	}
}
func TestZIPLayout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		ok      bool
	}{
		{"valid", []string{"github-copilot.so"}, true},
		{"empty", nil, false},
		{"macos", []string{"github-copilot.dylib"}, true},
		{"windows", []string{"github-copilot.dll"}, true},
		{"windows-wrong-library", []string{"github-copilot.so"}, false},
		{"removed-notices", []string{"github-copilot.so", "THIRD_PARTY_NOTICES.txt"}, false},
		{"nested", []string{"folder/github-copilot.so"}, false},
		{"extra", []string{"github-copilot.so", "another.so"}, false},
		{"duplicate", []string{"github-copilot.so", "github-copilot.so"}, false},
		{"traversal", []string{"../github-copilot.so"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.zip")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(f)
			for _, name := range tc.members {
				part, err := writer.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = part.Write([]byte("fixture")); err != nil {
					t.Fatal(err)
				}
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			goos := "linux"
			switch {
			case tc.name == "macos":
				goos = "darwin"
			case strings.HasPrefix(tc.name, "windows"):
				goos = "windows"
			}
			if err = checkZIP(path, goos); (err == nil) != tc.ok {
				t.Fatalf("checkZIP=%v want valid=%v", err, tc.ok)
			}
		})
	}
}
