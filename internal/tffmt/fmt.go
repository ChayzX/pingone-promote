// Package tffmt is the terraform fmt gate (ADR 0006): it pipes content
// through the real terraform (or tofu) formatter in a sealed environment
// and requires the output to equal the input.
package tffmt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// MaxFileSize bounds formatted content.
const MaxFileSize = 32 << 20

// FormatTimeout bounds one formatter run.
const FormatTimeout = 30 * time.Second

// DefaultFormatters are tried in order when no binary path is configured.
var DefaultFormatters = []string{"terraform", "tofu"}

// FormatterNotFoundError means no terraform or tofu binary could be run.
type FormatterNotFoundError struct{ Tried []string }

func (e *FormatterNotFoundError) Error() string {
	return "terraform fmt check: no formatter binary found (tried terraform, tofu or the configured path)"
}

// FormatterFailedError means the formatter ran but did not succeed, for
// example because the input is not valid HCL or the run timed out. Output
// from the child process is not included because it can quote the input.
type FormatterFailedError struct{ Err error }

func (e *FormatterFailedError) Error() string {
	return "terraform fmt check: formatter did not succeed: " + e.Err.Error()
}
func (e *FormatterFailedError) Unwrap() error { return e.Err }

// FormatMismatchError means terraform fmt (or hclwrite) would change the
// content, so the generated file is not canonical. Line is the first differing
// line (1-based).
type FormatMismatchError struct {
	Formatter string
	Line      int
}

func (e *FormatMismatchError) Error() string {
	return "terraform fmt check: " + e.Formatter + " output differs from generated content at line " + strconv.Itoa(e.Line)
}

// ResolveFormatter returns the absolute formatter path. An empty binPath
// searches PATH for DefaultFormatters.
func ResolveFormatter(binPath string) (string, error) {
	candidates := DefaultFormatters
	if binPath != "" {
		candidates = []string{binPath}
	}
	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}
	return "", &FormatterNotFoundError{Tried: candidates}
}

// formatterEnv is the complete child environment. Nothing is inherited: the
// child gets an empty CLI config, disabled checkpoint calls, and proxies that
// point at the discard port on loopback, so any network attempt fails fast.
// terraform fmt itself never needs the network.
func formatterEnv(home string) []string { return SealedEnv(home) }

// SealedEnv is the formatter child's complete environment (see formatterEnv).
func SealedEnv(home string) []string {
	blackhole := "http://127.0.0.1:9"
	return []string{
		"HOME=" + home,
		"PATH=",
		"TF_CLI_CONFIG_FILE=" + filepath.Join(home, "empty.tfrc"),
		"TF_CLI_ARGS=",
		"TF_IN_AUTOMATION=1",
		"CHECKPOINT_DISABLE=1",
		"TF_PLUGIN_CACHE_DIR=",
		"HTTP_PROXY=" + blackhole,
		"HTTPS_PROXY=" + blackhole,
		"http_proxy=" + blackhole,
		"https_proxy=" + blackhole,
		"NO_PROXY=",
		"no_proxy=",
	}
}

// FormatWithTerraform pipes content through `<bin> fmt -no-color -` and
// returns the formatter's output. It also checks that the output equals both
// content and hclwrite.Format(content); a difference returns the formatted
// bytes together with a *FormatMismatchError. binPath may name terraform or
// tofu, or be empty to search PATH.
func FormatWithTerraform(ctx context.Context, binPath string, content []byte) ([]byte, error) {
	if len(content) > MaxFileSize {
		return nil, &FormatterFailedError{Err: errors.New("content exceeds the maximum size")}
	}
	bin, err := ResolveFormatter(binPath)
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "pingone-promote-fmt-")
	if err != nil {
		return nil, &FormatterFailedError{Err: errors.New("could not create a private working directory")}
	}
	defer os.RemoveAll(home)
	if err := os.WriteFile(filepath.Join(home, "empty.tfrc"), nil, 0o600); err != nil {
		return nil, &FormatterFailedError{Err: errors.New("could not write the empty CLI configuration")}
	}
	ctx, cancel := context.WithTimeout(ctx, FormatTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "fmt", "-no-color", "-")
	cmd.Dir = home
	cmd.Env = formatterEnv(home)
	cmd.Stdin = bytes.NewReader(content)
	var stdout limitedBuffer
	stdout.limit = MaxFileSize + 1<<20
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &FormatterFailedError{Err: errors.New("timed out")}
		}
		return nil, &FormatterFailedError{Err: errors.New("exited with an error (is the input valid HCL?)")}
	}
	if stdout.overflow {
		return nil, &FormatterFailedError{Err: errors.New("output exceeds the size limit")}
	}
	out := stdout.Bytes()
	if line := firstDiffLine(out, content); line > 0 {
		return out, &FormatMismatchError{Formatter: filepath.Base(bin) + " fmt", Line: line}
	}
	if line := firstDiffLine(hclwrite.Format(content), content); line > 0 {
		return out, &FormatMismatchError{Formatter: "hclwrite.Format", Line: line}
	}
	return out, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// firstDiffLine returns the first 1-based line where a and b differ, or 0.
func firstDiffLine(a, b []byte) int {
	if bytes.Equal(a, b) {
		return 0
	}
	al, bl := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	for i := 0; i < len(al) || i < len(bl); i++ {
		if i >= len(al) || i >= len(bl) || !bytes.Equal(al[i], bl[i]) {
			return i + 1
		}
	}
	return 1
}
