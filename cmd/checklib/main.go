// Command checklib validates that a freshly built CLIProxyAPI plugin library
// actually matches its declared GOOS/GOARCH and exports the native entry
// point, before goreleaser archives it into a release asset.
//
// This exists because native c-shared libraries are built once per platform
// on separate CI runners (Go cannot cross-compile cgo the way it cross-
// compiles pure Go), so a mislabeled or corrupted artifact would otherwise
// only surface after it has already been shipped.
package main

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"flag"
	"fmt"
	"os"
)

// entryPoint is the C function CLIProxyAPI calls to load the plugin.
// It is defined in cmd/plugin/bridge.c and must be present and exported.
const entryPoint = "cliproxy_plugin_init"

// supportedTargets mirrors CLIProxyAPI's plugin platform support: Go
// c-shared builds work on FreeBSD only for AMD64.
var supportedTargets = map[string]bool{
	"darwin/amd64":  true,
	"darwin/arm64":  true,
	"linux/amd64":   true,
	"linux/arm64":   true,
	"freebsd/amd64": true,
}

func main() {
	path := flag.String("path", "", "path to the built plugin library")
	goos := flag.String("goos", "", "expected GOOS (darwin, linux, or freebsd)")
	goarch := flag.String("goarch", "", "expected GOARCH (amd64 or arm64)")
	flag.Parse()
	if *path == "" || *goos == "" || *goarch == "" {
		fmt.Fprintln(os.Stderr, "usage: checklib -path <library> -goos <goos> -goarch <goarch>")
		os.Exit(2)
	}
	if err := validateLibrary(*path, *goos, *goarch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s is a valid %s/%s plugin library\n", *path, *goos, *goarch)
}

func validateLibrary(path, goos, arch string) error {
	if !supportedTargets[goos+"/"+arch] {
		return fmt.Errorf("unsupported plugin target: %s/%s", goos, arch)
	}
	if goos == "linux" || goos == "freebsd" {
		return validateELF(path, goos, arch)
	}
	return validateMachO(path, arch)
}

func validateELF(path, goos, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if goos == "freebsd" && f.OSABI != elf.ELFOSABI_FREEBSD {
		return fmt.Errorf("%s does not declare the FreeBSD ELF ABI", path)
	}
	if goos == "linux" && f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("%s does not declare a Linux-compatible ELF ABI", path)
	}
	want := elf.EM_X86_64
	if arch == "arm64" {
		want = elf.EM_AARCH64
	}
	if f.Type != elf.ET_DYN || f.Machine != want || f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB {
		return fmt.Errorf("%s is not a %s shared library", path, arch)
	}
	return requireSymbol(path, entryPoint)
}

func validateMachO(path, arch string) error {
	f, err := macho.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	want := macho.CpuAmd64
	if arch == "arm64" {
		want = macho.CpuArm64
	}
	if f.Type != macho.TypeDylib || f.Cpu != want {
		return fmt.Errorf("%s is not a %s dynamic library", path, arch)
	}
	// Mach-O prefixes C symbols with an underscore.
	return requireSymbol(path, "_"+entryPoint)
}

// requireSymbol reports whether name appears in path's raw bytes. A linked
// shared library keeps every exported symbol's name as a plain ASCII string
// in its string table, so a byte scan is a simple, format-agnostic stand-in
// for parsing the ELF dynamic symbol table or the Mach-O symtab precisely.
func requireSymbol(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte(name)) {
		return fmt.Errorf("%s does not export %s", path, name)
	}
	return nil
}
