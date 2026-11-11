package exporter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"pingone-promote/internal/resourcecatalog"
	"pingone-promote/internal/secretscan"
)

const providerReaderTerraformVersion = "1.16.7"

// ProviderConfig pins the executable and official platform provider archive.
// Both SHA-256 digests are mandatory; only the pinned catalog version is used.
type ProviderConfig struct {
	Terraform, TerraformSHA256, ProviderZip, ProviderSHA256, ProviderVersion string
}

// ProviderStateReader imports source configuration into disposable local state.
// It never runs plan/apply and never accepts a caller's Terraform configuration.
type ProviderStateReader struct {
	Config    ProviderConfig
	Network   Network
	Timeout   time.Duration
	MaxOutput int64
}

// ProviderReadError deliberately excludes subprocess output and underlying
// errors: provider diagnostics can contain source credentials and state values.
type ProviderReadError struct{ Operation, Address string }

func (e *ProviderReadError) Error() string {
	if e.Address != "" {
		return "provider reader: " + e.Operation + " failed for " + e.Address
	}
	return "provider reader: " + e.Operation + " failed"
}
func providerReadFailure(op, address string) error {
	return &ProviderReadError{Operation: op, Address: address}
}

func (c ProviderConfig) validate() bool {
	return c.Terraform != "" && c.ProviderZip != "" && sha256RE.MatchString(c.TerraformSHA256) && sha256RE.MatchString(c.ProviderSHA256) && c.ProviderVersion == resourcecatalog.ProviderVersion() && c.ProviderVersion == "1.24.0"
}

// Read uses exact root addresses and full recipe import IDs. State is read into
// memory, rendered using the provider's schema, and removed on every exit path.
func (r *ProviderStateReader) Read(ctx context.Context, creds Credentials, resources []SourceResource) (*Export, error) {
	if !r.Config.validate() {
		return nil, providerReadFailure("configuration", "")
	}
	if creds.validate() != nil {
		return nil, providerReadFailure("credentials", "")
	}
	imports := make(map[string]string, len(resources))
	for _, item := range resources {
		address := item.String()
		if !addressRE.MatchString(address) || !providerIdentifier.MatchString(item.Label) || !resourcecatalog.Allowed(item.Type) {
			return nil, providerReadFailure("resource selection", "")
		}
		if _, exists := imports[address]; exists || !providerImportID(item.ImportID, creds.ExportEnvironment()) {
			return nil, providerReadFailure("resource selection", address)
		}
		imports[address] = item.ImportID
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	root, err := os.MkdirTemp("", "pingone-promote-provider-")
	if err != nil {
		return nil, providerReadFailure("workspace", "")
	}
	defer os.RemoveAll(root)
	if os.Chmod(root, 0700) != nil {
		return nil, providerReadFailure("workspace", "")
	}
	home := filepath.Join(root, "home")
	if os.Mkdir(home, 0700) != nil {
		return nil, providerReadFailure("workspace", "")
	}
	bin := filepath.Join(root, "terraform")
	if !providerCopyPinned(ctx, r.Config.Terraform, bin, r.Config.TerraformSHA256, 0700) {
		return nil, providerReadFailure("terraform checksum", "")
	}
	mirror := filepath.Join(root, "mirror", "registry.terraform.io", "pingidentity", "pingone")
	if !providerMkdirAll(mirror) {
		return nil, providerReadFailure("workspace", "")
	}
	archive := filepath.Join(mirror, fmt.Sprintf("terraform-provider-pingone_%s_%s_%s.zip", r.Config.ProviderVersion, runtime.GOOS, runtime.GOARCH))
	if !providerCopyPinned(ctx, r.Config.ProviderZip, archive, r.Config.ProviderSHA256, 0600) {
		return nil, providerReadFailure("provider checksum", "")
	}
	cli := filepath.Join(root, "terraform.rc")
	config := fmt.Sprintf("disable_checkpoint = true\nprovider_installation {\n  filesystem_mirror {\n    path = %q\n    include = [\"registry.terraform.io/pingidentity/pingone\"]\n  }\n}\n", filepath.Join(root, "mirror"))
	lock := fmt.Sprintf("provider \"registry.terraform.io/pingidentity/pingone\" {\n  version = \"%s\"\n  constraints = \"%s\"\n  hashes = [\"zh:%s\"]\n}\n", r.Config.ProviderVersion, r.Config.ProviderVersion, r.Config.ProviderSHA256)
	main := fmt.Sprintf("terraform {\n  required_version = \"= %s\"\n  required_providers {\n    pingone = { source = \"pingidentity/pingone\", version = \"= %s\" }\n  }\n}\nprovider \"pingone\" {}\n", providerReaderTerraformVersion, r.Config.ProviderVersion)
	for name, data := range map[string]string{"terraform.rc": config, ".terraform.lock.hcl": lock, "main.tf": main} {
		if os.WriteFile(filepath.Join(root, name), []byte(data), 0600) != nil {
			return nil, providerReadFailure("workspace", "")
		}
	}
	remaining := r.MaxOutput
	if remaining <= 0 {
		remaining = 256 << 20
	}
	run := func(op, address string, args []string, c *Credentials) ([]byte, error) {
		// Recheck the private executable before every invocation, including version.
		if !providerMatches(bin, r.Config.TerraformSHA256) {
			return nil, providerReadFailure("terraform checksum", "")
		}
		commandCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
		defer stop()
		cmd := exec.CommandContext(commandCtx, bin, args...)
		if providerContainProcess(cmd) != nil {
			return nil, providerReadFailure("process containment", "")
		}
		cmd.Dir = root
		cmd.Env = r.providerEnv(home, cli, c)
		cmd.WaitDelay = time.Second
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Cancel()
			}
		}()
		cap := &providerOutput{remaining: remaining, cancel: stop}
		cmd.Stdout = &providerOutputStream{output: cap, retain: true}
		cmd.Stderr = &providerOutputStream{output: cap}
		err := cmd.Run()
		remaining -= cap.used
		if err != nil || cap.exceeded || commandCtx.Err() != nil || !providerPrivateTree(root) {
			return nil, providerReadFailure(op, address)
		}
		return cap.data.Bytes(), nil
	}
	version, err := run("version", "", []string{"--version"}, nil)
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(string(version), "\n")
	if strings.TrimSpace(first) != "Terraform v"+providerReaderTerraformVersion {
		return nil, providerReadFailure("version", "")
	}
	if _, err = run("init", "", []string{"init", "-backend=false", "-input=false"}, nil); err != nil {
		return nil, err
	}
	schema, err := run("schema", "", []string{"providers", "schema", "-json"}, nil)
	if err != nil {
		return nil, err
	}
	blocks, err := providerImportBlocks(schema, resources, creds.ExportEnvironment())
	if err != nil {
		return nil, providerReadFailure("schema", "")
	}
	if os.WriteFile(filepath.Join(root, "resources.tf"), blocks, 0600) != nil {
		return nil, providerReadFailure("workspace", "")
	}
	for _, address := range sortedKeys(imports) {
		if _, err = run("import", address, []string{"import", "-input=false", "-no-color", address, imports[address]}, &creds); err != nil {
			return nil, err
		}
	}
	var state []byte
	if len(imports) > 0 {
		state, err = run("show", "", []string{"show", "-json"}, nil)
		if err != nil {
			return nil, err
		}
	} else {
		state = []byte(`{"values":{"root_module":{"resources":[]}}}`)
	}
	result, err := RenderProviderState(schema, state, imports)
	if err != nil {
		return nil, providerReadFailure("render", "")
	}
	result.EnvironmentID = creds.ExportEnvironment()
	result.Findings = secretscan.ScanFiles(result.Files)
	return result, nil
}

func providerImportID(id, env string) bool {
	if len(id) > 4096 {
		return false
	}
	parts := strings.Split(id, "/")
	if len(parts) < 1 || parts[0] != env {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 512 {
			return false
		}
		for _, c := range part {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == ':') {
				return false
			}
		}
	}
	return true
}

func (r *ProviderStateReader) providerEnv(home, cli string, c *Credentials) []string {
	env := []string{"HOME=" + home, "PATH=", "TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1", "TF_CLI_CONFIG_FILE=" + cli}
	if c != nil {
		env = append(env, "PINGONE_CLIENT_ID="+c.ClientID, "PINGONE_CLIENT_SECRET="+c.ClientSecret, "PINGONE_ENVIRONMENT_ID="+c.WorkerEnvironmentID, "PINGONE_REGION_CODE="+c.RegionCode)
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
func providerMkdirAll(dir string) bool {
	if os.MkdirAll(dir, 0700) != nil {
		return false
	}
	return true
}
func providerCopyPinned(ctx context.Context, src, dst, digest string, mode os.FileMode) bool {
	f, err := os.Open(src)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return false
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return false
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		if ctx.Err() != nil {
			out.Close()
			return false
		}
		n, e := f.Read(buf)
		if n > 0 {
			if _, err := io.MultiWriter(out, h).Write(buf[:n]); err != nil {
				out.Close()
				return false
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			out.Close()
			return false
		}
	}
	if out.Close() != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}
func providerMatches(path, digest string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}
func providerPrivateTree(root string) bool {
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported workspace entry")
		}
		mode := os.FileMode(0600)
		if d.IsDir() || info.Mode()&0111 != 0 {
			mode = 0700
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported workspace entry")
		}
		return os.Chmod(path, mode)
	})
	return err == nil
}

type providerOutput struct {
	mu              sync.Mutex
	data            bytes.Buffer
	remaining, used int64
	exceeded        bool
	cancel          context.CancelFunc
}
type providerOutputStream struct {
	output *providerOutput
	retain bool
}

func (w *providerOutputStream) Write(p []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	room := o.remaining - o.used
	if int64(len(p)) > room {
		o.exceeded = true
		o.cancel()
		if room < 0 {
			room = 0
		}
		p = p[:int(room)]
	}
	o.used += int64(len(p))
	if w.retain {
		o.data.Write(p)
	}
	return n, nil
}

// CLI import requires a resource address but does not validate its planned
// arguments. Environment scope is supplied only where the actual schema allows
// it; required values are hydrated by the provider's read operation.
func providerImportBlocks(schema []byte, resources []SourceResource, env string) ([]byte, error) {
	var doc struct {
		Providers map[string]struct {
			Resources map[string]struct {
				Block struct {
					Attributes map[string]json.RawMessage `json:"attributes"`
				} `json:"block"`
			} `json:"resource_schemas"`
		} `json:"provider_schemas"`
	}
	if decodeProviderJSON(schema, &doc) != nil {
		return nil, fmt.Errorf("invalid schema")
	}
	provider, ok := doc.Providers["registry.terraform.io/pingidentity/pingone"]
	if !ok {
		return nil, fmt.Errorf("missing schema")
	}
	file := hclwrite.NewEmptyFile()
	for _, item := range resources {
		s, ok := provider.Resources[item.Type]
		if !ok {
			return nil, fmt.Errorf("missing resource schema")
		}
		body := file.Body().AppendNewBlock("resource", []string{item.Type, item.Label}).Body()
		if _, ok := s.Block.Attributes["environment_id"]; ok {
			body.SetAttributeValue("environment_id", cty.StringVal(env))
		}
	}
	return file.Bytes(), nil
}
