// Command unzip-one extracts one file from a zip archive. CI images built
// on golang have no unzip; this is used to unpack a checksum-verified
// terraform release in examples/gitlab-ci/pingone-promote.yml.
//
//	go run ./scripts/unzip-one ARCHIVE NAME DEST
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: unzip-one ARCHIVE NAME DEST")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "unzip-one:", err)
		os.Exit(1)
	}
}

func run(archive, name, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > 1<<30 {
			return fmt.Errorf("%s is too large", name)
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(src, 1<<30)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("%s not found in %s", name, archive)
}
