package main

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLibraryAcceptsMatchingTargets(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		machine := elf.EM_X86_64
		if arch == "arm64" {
			machine = elf.EM_AARCH64
		}
		linux := filepath.Join(t.TempDir(), "github-copilot.so")
		writeELFFixture(t, linux, elf.ELFOSABI_NONE, machine, true)
		if err := validateLibrary(linux, "linux", arch); err != nil {
			t.Fatalf("linux/%s: %v", arch, err)
		}
		darwin := filepath.Join(t.TempDir(), "github-copilot.dylib")
		writeMachOFixture(t, darwin, arch, true)
		if err := validateLibrary(darwin, "darwin", arch); err != nil {
			t.Fatalf("darwin/%s: %v", arch, err)
		}
	}
	freebsd := filepath.Join(t.TempDir(), "github-copilot.so")
	writeELFFixture(t, freebsd, elf.ELFOSABI_FREEBSD, elf.EM_X86_64, true)
	if err := validateLibrary(freebsd, "freebsd", "amd64"); err != nil {
		t.Fatalf("freebsd/amd64: %v", err)
	}
}

func TestValidateLibraryRejectsWrongELFOperatingSystemAndUnsupportedTarget(t *testing.T) {
	linux := filepath.Join(t.TempDir(), "linux.so")
	bsd := filepath.Join(t.TempDir(), "freebsd.so")
	writeELFFixture(t, linux, elf.ELFOSABI_NONE, elf.EM_X86_64, true)
	writeELFFixture(t, bsd, elf.ELFOSABI_FREEBSD, elf.EM_X86_64, true)
	if err := validateLibrary(linux, "freebsd", "amd64"); err == nil {
		t.Fatal("Linux library mislabeled as FreeBSD")
	}
	if err := validateLibrary(bsd, "linux", "amd64"); err == nil {
		t.Fatal("FreeBSD library mislabeled as Linux")
	}
	if err := validateLibrary(bsd, "freebsd", "arm64"); err == nil {
		t.Fatal("unsupported FreeBSD ARM64 target accepted")
	}
}

func TestValidateLibraryRejectsMissingEntryPoint(t *testing.T) {
	linux := filepath.Join(t.TempDir(), "github-copilot.so")
	writeELFFixture(t, linux, elf.ELFOSABI_NONE, elf.EM_X86_64, false)
	if err := validateLibrary(linux, "linux", "amd64"); err == nil {
		t.Fatal("library without the plugin entry point accepted")
	}
	darwin := filepath.Join(t.TempDir(), "github-copilot.dylib")
	writeMachOFixture(t, darwin, "arm64", false)
	if err := validateLibrary(darwin, "darwin", "arm64"); err == nil {
		t.Fatal("library without the plugin entry point accepted")
	}
}

// Minimal file-format fixtures test metadata validation, not executable
// code. requireSymbol only scans raw bytes, so appending the entry-point
// name's bytes after the header is enough to stand in for a real symbol
// table entry.
func writeELFFixture(t *testing.T, path string, abi elf.OSABI, machine elf.Machine, withEntryPoint bool) {
	t.Helper()
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, byte(abi)})
	binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_DYN))
	binary.LittleEndian.PutUint16(data[18:], uint16(machine))
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	binary.LittleEndian.PutUint16(data[58:], 64)
	if withEntryPoint {
		data = append(data, []byte(entryPoint)...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeMachOFixture(t *testing.T, path, arch string, withEntryPoint bool) {
	t.Helper()
	data := make([]byte, 32)
	cpu := macho.CpuAmd64
	if arch == "arm64" {
		cpu = macho.CpuArm64
	}
	binary.LittleEndian.PutUint32(data, macho.Magic64)
	binary.LittleEndian.PutUint32(data[4:], uint32(cpu))
	binary.LittleEndian.PutUint32(data[12:], uint32(macho.TypeDylib))
	if withEntryPoint {
		data = append(data, []byte("_"+entryPoint)...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
