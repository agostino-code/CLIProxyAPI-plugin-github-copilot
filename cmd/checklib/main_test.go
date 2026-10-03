package main

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
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
	windows := filepath.Join(t.TempDir(), "github-copilot.dll")
	writePEFixture(t, windows, pe.IMAGE_FILE_MACHINE_AMD64, imageFileDLL, true)
	if err := validateLibrary(windows, "windows", "amd64"); err != nil {
		t.Fatalf("windows/amd64: %v", err)
	}
}

func TestValidateLibraryRejectsInvalidWindowsLibraries(t *testing.T) {
	dir := t.TempDir()
	arm := filepath.Join(dir, "arm64.dll")
	writePEFixture(t, arm, pe.IMAGE_FILE_MACHINE_ARM64, imageFileDLL, true)
	if err := validateLibrary(arm, "windows", "amd64"); err == nil {
		t.Fatal("ARM64 DLL accepted as windows/amd64")
	}
	exe := filepath.Join(dir, "plugin.exe")
	writePEFixture(t, exe, pe.IMAGE_FILE_MACHINE_AMD64, 0, true)
	if err := validateLibrary(exe, "windows", "amd64"); err == nil {
		t.Fatal("executable accepted as a DLL")
	}
	noEntry := filepath.Join(dir, "noentry.dll")
	writePEFixture(t, noEntry, pe.IMAGE_FILE_MACHINE_AMD64, imageFileDLL, false)
	if err := validateLibrary(noEntry, "windows", "amd64"); err == nil {
		t.Fatal("DLL without the plugin entry point accepted")
	}
	if err := validateLibrary(noEntry, "windows", "arm64"); err == nil {
		t.Fatal("unsupported windows/arm64 target accepted")
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

func writePEFixture(t *testing.T, path string, machine uint16, characteristics uint16, withEntryPoint bool) {
	t.Helper()
	const signature = 0x80
	data := make([]byte, signature+4+20)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], signature)
	copy(data[signature:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(data[signature+4:], machine)
	binary.LittleEndian.PutUint16(data[signature+4+18:], characteristics)
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
