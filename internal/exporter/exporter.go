// Package exporter runs a pinned pingcli-plugin-terraformer binary as a
// subprocess to export ONE source PingOne environment (ADR 0015).
//
// The CLI contract below was read from the terraformer source at tag v0.6.0
// (commit 9aa51ed), https://github.com/pingidentity/pingcli-plugin-terraformer:
//
//   - main.go: the binary runs in standalone mode unless
//     PLUGIN_PROTOCOL_VERSIONS is set (then it serves the pingcli gRPC
//     plugin). We never set it, so `pingcli-terraformer export [flags]` runs
//     standalone. `--version` prints
//     "pingcli-terraformer version <v> (commit: <sha>)".
//   - cmd/tf.go: subcommands export and list-outputs.
//   - cmd/export.go: flags --out, --module-dir (default ping-export-module),
//     --module-name (default ping-export), --include-imports,
//     --include-values, --output-format hcl|tfjson, --include-resources and
//     --exclude-resources (pflag StringSlice: repeatable AND split on
//     commas), --list-resources (prints "<type>.<label>" lines on stdout and
//     exits), --include-upstream, --skip-dependencies. Credentials come from
//     flags or PINGCLI_PINGONE_ENVIRONMENT_ID, PINGCLI_PINGONE_EXPORT_ENVIRONMENT_ID,
//     PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_ID,
//     PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_SECRET and
//     PINGCLI_PINGONE_REGION_CODE. We use only the environment variables, so
//     secrets never appear on argv.
//   - internal/module/generator.go: output layout (<module-dir>/ with one
//     <type>.tf per resource type, variables.tf, versions.tf, outputs.tf;
//     <module-name>-module.tf, -variables.tf, -imports.tf and
//     -terraform.auto.tfvars at the root).
//   - internal/core/orchestrator.go assignLabels: duplicate labels are an
//     error ("duplicate resource labels") unless the type sets
//     allow_duplicate_labels (only flow policies do, definitions/pingone/davinci/flow_policy.yaml).
//   - internal/utils/sanitize.go: label and variable-name sanitization.
//
// Every exec verifies the binary first: the configured file is copied into a
// private 0700 directory, the copy's SHA-256 must equal Config.SHA256 (when
// one is set), and the copy's --version output must report Config.Version.
// Only the verified copy is executed, so the file cannot change between
// check and use. An empty Config.SHA256 skips only the digest comparison:
// CI that builds terraformer from a pinned commit through the Go checksum
// database already pins the source, and the version check still runs.
package exporter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/secretscan"
)

// Config pins the terraformer binary.
type Config struct {
	Version string `json:"version"` // e.g. "0.6.0" (a leading "v" is ignored)
	SHA256  string `json:"sha256"`  // hex digest of the binary; "" skips the digest check
	Path    string `json:"path"`    // the binary
}

// Credentials are the read-only worker credentials for the SOURCE env.
type Credentials struct {
	ClientID            string
	ClientSecret        string
	WorkerEnvironmentID string // environment holding the worker app
	ExportEnvironmentID string // environment to export; "" = worker env
	RegionCode          string // AP, AU, CA, EU, NA or SG
}

// Network is optional egress configuration passed to the child.
type Network struct {
	HTTPSProxy string // HTTPS_PROXY
	NoProxy    string // NO_PROXY
	CAFile     string // SSL_CERT_FILE
}

// Runner executes the pinned binary.
type Runner struct {
	Config  Config
	Network Network
	// Timeout bounds one export (default 15 minutes).
	Timeout time.Duration
	// MaxOutput bounds the total size of the export (default 256 MiB).
	MaxOutput int64
}

// exeSuffix keeps the staged copy executable on a Windows development
// machine. Windows is not a supported platform.
var exeSuffix = map[bool]string{true: ".exe"}[runtime.GOOS == "windows"]

// Errors.
var (
	ErrChecksum = errors.New("exporter: terraformer binary checksum does not match the pinned sha256")
	ErrVersion  = errors.New("exporter: terraformer binary does not report the pinned version")
)

// ExecError is a failed terraformer run. Stderr is truncated and has the
// client secret removed; it is diagnostic data.
type ExecError struct {
	ExitCode int
	Stderr   string
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("exporter: terraformer exited with status %d: %s", e.ExitCode, e.Stderr)
}

// LabelCollisionError is terraformer's own duplicate-label failure, for
// example two flow-scoped DaVinci variables with the same name on different
// flows (label_fields [name, context]). Nothing was exported.
type LabelCollisionError struct{ Detail string }

func (e *LabelCollisionError) Error() string {
	return "exporter: terraformer refused to export because two resources produce the same Terraform label (upstream issue: flow-scoped variable labels need the flow); " + e.Detail
}

var (
	versionRE = regexp.MustCompile(`^pingcli-terraformer version (\S+) \(commit: (\S+)\)\s*$`)
	uuidRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// Resource patterns: addresses and globs. No commas (pflag splits on
	// them) and no "regex:" patterns.
	patternRE = regexp.MustCompile(`^[A-Za-z0-9_*?\-]+(\.[A-Za-z0-9_*?\-]+)?$`)
	addressRE = regexp.MustCompile(`^(pingone_[a-z0-9_]+)\.([A-Za-z0-9_\-]+)$`)
	regions   = map[string]bool{"AP": true, "AU": true, "CA": true, "EU": true, "NA": true, "SG": true}
)

func (c Config) validate() error {
	if strings.TrimSpace(c.Path) == "" {
		return errors.New("exporter: terraformer path is required")
	}
	if c.SHA256 != "" && !sha256RE.MatchString(c.SHA256) {
		return errors.New("exporter: terraformer sha256 must be empty or 64 lowercase hex characters")
	}
	if strings.TrimPrefix(c.Version, "v") == "" {
		return errors.New("exporter: terraformer version is required")
	}
	return nil
}

func (c Credentials) validate() error {
	switch {
	case c.ClientID == "" || strings.ContainsAny(c.ClientID, "\x00\n\r"):
		return errors.New("exporter: worker client ID is required")
	case c.ClientSecret == "" || strings.ContainsAny(c.ClientSecret, "\x00\n\r"):
		return errors.New("exporter: worker client secret is required")
	case !uuidRE.MatchString(c.WorkerEnvironmentID):
		return errors.New("exporter: worker environment ID must be a PingOne environment ID")
	case c.ExportEnvironmentID != "" && !uuidRE.MatchString(c.ExportEnvironmentID):
		return errors.New("exporter: export environment ID must be a PingOne environment ID")
	case !regions[c.RegionCode]:
		return errors.New("exporter: region code must be one of AP, AU, CA, EU, NA, SG")
	}
	return nil
}

// ExportEnvironment is the environment actually exported.
func (c Credentials) ExportEnvironment() string {
	if c.ExportEnvironmentID != "" {
		return c.ExportEnvironmentID
	}
	return c.WorkerEnvironmentID
}

// childEnv is the COMPLETE environment of the child. Nothing is inherited:
// no TF_VAR_*, GitLab tokens, keychain or CI variables reach terraformer.
func (r *Runner) childEnv(home string, creds *Credentials) []string {
	env := []string{"HOME=" + home}
	if creds != nil {
		env = append(env,
			"PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_ID="+creds.ClientID,
			"PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_SECRET="+creds.ClientSecret,
			"PINGCLI_PINGONE_ENVIRONMENT_ID="+creds.WorkerEnvironmentID,
			"PINGCLI_PINGONE_EXPORT_ENVIRONMENT_ID="+creds.ExportEnvironment(),
			"PINGCLI_PINGONE_REGION_CODE="+creds.RegionCode,
		)
	}
	if r.Network.HTTPSProxy != "" {
		env = append(env, "HTTPS_PROXY="+r.Network.HTTPSProxy)
	}
	if r.Network.NoProxy != "" {
		env = append(env, "NO_PROXY="+r.Network.NoProxy)
	}
	if r.Network.CAFile != "" {
		env = append(env, "SSL_CERT_FILE="+r.Network.CAFile)
	}
	return env
}

// AllowedEnv lists every variable name the child may receive.
var AllowedEnv = []string{
	"HOME",
	"PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_ID",
	"PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_SECRET",
	"PINGCLI_PINGONE_ENVIRONMENT_ID",
	"PINGCLI_PINGONE_EXPORT_ENVIRONMENT_ID",
	"PINGCLI_PINGONE_REGION_CODE",
	"HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE",
}

// workspace is one private run directory.
type workspace struct {
	root, home, out, bin string
}

func newWorkspace() (*workspace, error) {
	root, err := os.MkdirTemp("", "pingone-promote-export-")
	if err != nil {
		return nil, errors.New("exporter: could not create a private working directory")
	}
	w := &workspace{root: root, home: filepath.Join(root, "home"), out: filepath.Join(root, "out"), bin: filepath.Join(root, "bin", "pingcli-terraformer"+exeSuffix)}
	for _, d := range []string{w.home, w.out, filepath.Dir(w.bin)} {
		if err := os.Mkdir(d, 0o700); err != nil {
			os.RemoveAll(root)
			return nil, errors.New("exporter: could not create a private working directory")
		}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	return w, nil
}

func (w *workspace) close() { os.RemoveAll(w.root) }

// install copies the configured binary into the workspace and checks the
// copy's digest.
func (r *Runner) install(w *workspace) error {
	src, err := os.Open(r.Config.Path)
	if err != nil {
		return fmt.Errorf("exporter: cannot open the terraformer binary: %w", err)
	}
	defer src.Close()
	if fi, err := src.Stat(); err != nil || !fi.Mode().IsRegular() {
		return errors.New("exporter: the terraformer path is not a regular file")
	}
	dst, err := os.OpenFile(w.bin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return errors.New("exporter: cannot stage the terraformer binary")
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return errors.New("exporter: cannot stage the terraformer binary")
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return r.verifyDigest(w)
}

func (r *Runner) verifyDigest(w *workspace) error {
	if r.Config.SHA256 == "" {
		return nil
	}
	f, err := os.Open(w.bin)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.Config.SHA256 {
		// A digest is not secret; printing it lets an operator pin a build.
		return fmt.Errorf("%w (the binary's sha256 is %s)", ErrChecksum, got)
	}
	return nil
}

type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// exec runs the verified copy with explicit argv and the allowlisted env.
// The digest is re-checked immediately before the exec.
func (r *Runner) exec(ctx context.Context, w *workspace, args []string, creds *Credentials, stdoutMax int) ([]byte, error) {
	if err := r.verifyDigest(w); err != nil {
		return nil, err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.bin, args...)
	cmd.Dir = w.root
	cmd.Env = r.childEnv(w.home, creds)
	stdout := &capped{max: stdoutMax}
	stderr := &capped{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("exporter: terraformer timed out")
		}
		msg := stderr.String()
		if creds != nil && creds.ClientSecret != "" {
			msg = strings.ReplaceAll(msg, creds.ClientSecret, "[redacted]")
		}
		if strings.Contains(msg, "duplicate resource labels") {
			return nil, &LabelCollisionError{Detail: lastLines(msg, 20)}
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return nil, &ExecError{ExitCode: code, Stderr: lastLines(msg, 20)}
	}
	return stdout.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// prepare stages and verifies the binary, including its version.
func (r *Runner) prepare(ctx context.Context) (*workspace, error) {
	if err := r.Config.validate(); err != nil {
		return nil, err
	}
	w, err := newWorkspace()
	if err != nil {
		return nil, err
	}
	if err := r.install(w); err != nil {
		w.close()
		return nil, err
	}
	out, err := r.exec(ctx, w, []string{"--version"}, nil, 4096)
	if err != nil {
		w.close()
		return nil, err
	}
	m := versionRE.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil || strings.TrimPrefix(m[1], "v") != strings.TrimPrefix(r.Config.Version, "v") {
		w.close()
		return nil, ErrVersion
	}
	return w, nil
}

// Verify checks the pinned binary without exporting anything.
func (r *Runner) Verify(ctx context.Context) error {
	w, err := r.prepare(ctx)
	if err != nil {
		return err
	}
	w.close()
	return nil
}

// Address is one resource address from --list-resources.
type Address struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

func (a Address) String() string { return a.Type + "." + a.Label }

// ListResources runs `export --list-resources`. terraformer prints one
// "<type>.<label>" per line; names and IDs are not included.
func (r *Runner) ListResources(ctx context.Context, creds Credentials) ([]Address, error) {
	if err := creds.validate(); err != nil {
		return nil, err
	}
	w, err := r.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer w.close()
	out, err := r.exec(ctx, w, []string{"export", "--list-resources"}, &creds, 16<<20)
	if err != nil {
		return nil, err
	}
	var list []Address
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m := addressRE.FindStringSubmatch(line)
		if m == nil {
			return nil, errors.New("exporter: unexpected --list-resources output line")
		}
		list = append(list, Address{Type: m[1], Label: m[2]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].String() < list[j].String() })
	return list, nil
}

// ExportOptions select what to export.
type ExportOptions struct {
	Include []string // --include-resources patterns or addresses
	// All exports every supported resource: no --include-resources. It is
	// the whole-environment scope (ADR 0015) and excludes Include.
	All bool
	// Exclude are --exclude-resources patterns. terraformer's explicit
	// excludes override upstream expansion (core.applyUpstreamExpansion), and
	// a reference to an excluded resource becomes a variable named
	// <type>_<label>_id (core.resolveOneReference) instead of a resource
	// reference.
	Exclude []string
	// IncludeUpstream adds everything the included resources reference.
	IncludeUpstream bool
	// Imports adds --include-imports so the export carries source IDs.
	Imports bool
	// SkipValues omits --include-values (values are needed to inline
	// shared literals).
	SkipValues bool
}

// Export is a completed export held in memory. The directory it came from
// has already been deleted.
type Export struct {
	EnvironmentID string
	ModuleDir     string
	ModuleName    string
	Files         map[string][]byte
	Findings      []secretscan.Finding
	// Referenced are resources the export refers to by ID but does not
	// contain (address -> source ID). Their references are ID variables
	// named neutralize.ExcludedVariableName(address), like excluded
	// resources. Only the combined exporter sets it.
	Referenced map[string]string
	// Adopt lists exported built-in objects that every environment must
	// adopt by import ID (they exist there already and must not be created).
	Adopt    []string
	Warnings []string
	// Owners maps an exported owned child (a sign-on policy action, an
	// application's attribute mapping) to its parent's address.
	Owners map[string]string
}

// Module directory and name we pass explicitly (terraformer's defaults).
const (
	ModuleDir  = "ping-export-module"
	ModuleName = "ping-export"
)

// Argv returns the export argv (without the binary).
func (o ExportOptions) Argv(outDir string) ([]string, error) {
	switch {
	case o.All && len(o.Include) > 0:
		return nil, errors.New("exporter: a whole-environment export takes no --include-resources pattern")
	case !o.All && len(o.Include) == 0:
		return nil, errors.New("exporter: at least one --include-resources pattern is required unless the whole environment is exported (All)")
	case o.All && o.IncludeUpstream:
		return nil, errors.New("exporter: --include-upstream has no meaning for a whole-environment export")
	}
	args := []string{"export", "--out", outDir, "--module-dir", ModuleDir, "--module-name", ModuleName, "--output-format", "hcl"}
	if !o.SkipValues {
		args = append(args, "--include-values")
	}
	if o.Imports {
		args = append(args, "--include-imports")
	}
	for _, p := range o.Include {
		if !patternRE.MatchString(p) {
			return nil, fmt.Errorf("exporter: resource pattern %q is not allowed (letters, digits, _ - * ? and one dot)", p)
		}
		args = append(args, "--include-resources", p)
	}
	if o.IncludeUpstream {
		args = append(args, "--include-upstream")
	}
	for _, p := range o.Exclude {
		if !patternRE.MatchString(p) {
			return nil, fmt.Errorf("exporter: resource pattern %q is not allowed", p)
		}
		args = append(args, "--exclude-resources", p)
	}
	return args, nil
}

// Export runs `export --include-resources … [--include-upstream]
// [--exclude-resources …] --output-format hcl` into a private directory,
// reads the output, scans it for secrets and deletes the directory.
func (r *Runner) Export(ctx context.Context, creds Credentials, o ExportOptions) (*Export, error) {
	if err := creds.validate(); err != nil {
		return nil, err
	}
	w, err := r.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer w.close()
	args, err := o.Argv(w.out)
	if err != nil {
		return nil, err
	}
	if _, err := r.exec(ctx, w, args, &creds, 1<<20); err != nil {
		return nil, err
	}
	max := r.MaxOutput
	if max <= 0 {
		max = 256 << 20
	}
	files, err := readTree(w.out, max)
	if err != nil {
		return nil, err
	}
	return &Export{EnvironmentID: creds.ExportEnvironment(), ModuleDir: ModuleDir, ModuleName: ModuleName,
		Files: files, Findings: secretscan.ScanFiles(files)}, nil
}

func readTree(dir string, max int64) (map[string][]byte, error) {
	files := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("exporter: export output contains a non-regular file")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > max || len(files) > 10000 {
			return errors.New("exporter: export output exceeds the size limit")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}
