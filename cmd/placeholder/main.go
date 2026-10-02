// Command placeholder is not part of any release. Open-source goreleaser's
// build step always needs a real Go package to compile per target; its
// output is immediately replaced by the real, natively built cgo c-shared
// plugin library through a build post-hook. See scripts/goreleaser-import.sh
// and the darwin/linux/freebsd build entries in .goreleaser.yml.
package main

func main() {}
