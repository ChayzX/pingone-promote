package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"pingone-promote/internal/tffmt"
)

// PrepareTools verifies and privately copies hosted executables. The caller
// must keep the copies until all background work has stopped, then clean up.
func PrepareTools(c Config) (Config, func(), error) {
	dir, err := os.MkdirTemp("", "pingone-promote-tools-")
	if err != nil {
		return c, nil, errors.New("cannot create private tool directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fail := func() (Config, func(), error) {
		cleanup()
		return c, nil, errors.New("a tool failed its pinned checksum or version check")
	}
	specs := []struct {
		path                 *string
		sum, version, prefix string
	}{
		{&c.Tools.Terraform, c.Tools.TerraformSHA256, c.Tools.TerraformVersion, "Terraform v"},
		{&c.Tools.Terraformer, c.Tools.TerraformerSHA256, strings.TrimPrefix(c.Tools.TerraformerVersion, "v"), "pingcli-terraformer version "},
	}
	if c.Tools.PingCLI != "" {
		specs = append(specs, struct {
			path                 *string
			sum, version, prefix string
		}{&c.Tools.PingCLI, c.Tools.PingCLISHA256, c.Tools.PingCLIVersion, "pingcli version "})
	}
	for i, spec := range specs {
		source, err := os.Open(*spec.path)
		if err != nil {
			return fail()
		}
		stat, err := source.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 512<<20 {
			source.Close()
			return fail()
		}
		out := filepath.Join(dir, []string{"terraform", "pingcli-terraformer", "pingcli"}[i])
		copyFile, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
		if err != nil {
			source.Close()
			return fail()
		}
		hash := sha256.New()
		_, err = io.Copy(io.MultiWriter(copyFile, hash), io.LimitReader(source, (512<<20)+1))
		source.Close()
		closeErr := copyFile.Close()
		if err != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != spec.sum {
			return fail()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, out, "--version")
		cmd.Dir = dir
		cmd.Env = tffmt.SealedEnv(dir)
		// Version output is never surfaced: a compromised tool could print credentials.
		var version boundedVersionOutput
		cmd.Stdout = &version
		runErr := cmd.Run()
		raw := version.Bytes()
		cancel()
		if runErr != nil || len(raw) > 4096 || !toolVersionMatches(string(raw), spec.prefix, spec.version) {
			return fail()
		}
		*spec.path = out
	}
	return c, cleanup, nil
}

func toolVersionMatches(output, prefix, version string) bool {
	if version == "" {
		return false
	}
	first := strings.SplitN(strings.TrimSpace(output), "\n", 2)[0]
	// Ping CLI creates its private first-run config before printing its version.
	// Match its named version line, never an arbitrary banner containing a number.
	if prefix == "pingcli version " {
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, prefix) {
				first = line
				break
			}
		}
	}

	rest, ok := strings.CutPrefix(first, prefix)
	if !ok {
		return false
	}
	fields := strings.Fields(rest)
	return len(fields) > 0 && strings.TrimPrefix(fields[0], "v") == strings.TrimPrefix(version, "v")
}

// boundedVersionOutput limits stdout before allocation, not after execution.
type boundedVersionOutput struct{ bytes.Buffer }

func (b *boundedVersionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("version output exceeded limit")
	}
	return b.Buffer.Write(p)
}
