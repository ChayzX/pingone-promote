// Package server is the hosted form of pingone-promote: one HTTP service
// that searches the source environment, previews a request, and commits it
// to a branch of the config repository (opening a merge request). The
// enterprise pipeline then plans and applies as usual; no CI job of this
// tool is involved. It keeps no data of its own: drafts live in memory and
// everything durable is in GitLab.
package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"

	"pingone-promote/internal/gitlab"
)

// Config is the non-secret configuration file (JSON).
type Config struct {
	// Listen is host:port. The default 127.0.0.1:8080 is reachable from
	// this machine only.
	Listen string `json:"listen"`
	// Exposure must be set to listen on anything but a loopback address:
	// "tls" (the server terminates TLS: tlsCertFile and tlsKeyFile) or
	// "behind-proxy" (a TLS proxy in front; allowedHosts names it). Phase 1
	// has no sign-in either way: anyone who reaches the port can file
	// requests under any name.
	Exposure string `json:"exposure,omitempty"`
	// TLSCertFile and TLSKeyFile turn on HTTPS. Leave both empty behind a
	// TLS-terminating reverse proxy, or on localhost.
	TLSCertFile string `json:"tlsCertFile,omitempty"`
	TLSKeyFile  string `json:"tlsKeyFile,omitempty"`
	// AllowedHosts are the Host header values the server answers
	// (hostnames, without a port). localhost and the loopback addresses are
	// always allowed.
	AllowedHosts []string `json:"allowedHosts,omitempty"`
	// SignIn is "none" (phase 1: people type a name, recorded as
	// unverified). "pingone" is reserved for PingOne sign-in.
	SignIn string `json:"signIn,omitempty"`
	// SecretsFile holds the three secrets (see LoadSecrets).
	SecretsFile string `json:"secretsFile"`

	GitLab struct {
		// BaseURL is the GitLab origin, for example https://gitlab.example.com.
		BaseURL   string   `json:"baseUrl"`
		ProjectID string   `json:"projectId"`
		MRLabels  []string `json:"mrLabels,omitempty"`
	} `json:"gitlab"`
	PingOne struct {
		RegionCode          string `json:"regionCode"`
		WorkerEnvironmentID string `json:"workerEnvironmentId"`
	} `json:"pingone"`
	// Sources are the environments a request may export from; Rollout is
	// the chain the pipeline applies after the source, in order. Names are
	// the tfvars file names (development -> development.tfvars).
	Sources []string `json:"sources"`
	// SourceEnvironmentIDs gives a source's PingOne environment ID, for a
	// repository whose tfvars do not set pingone_environment_id (it comes
	// from a CI variable). A source without one here is looked up in its
	// tfvars on the default branch.
	SourceEnvironmentIDs map[string]string `json:"sourceEnvironmentIds,omitempty"`
	// EnvironmentIDs gives every environment's PingOne environment ID
	// (sources and rollout). With it the server enters
	// pingone_environment_id where a tfvars file lacks it, and reads each
	// rollout environment (read-only, with the same worker) to fill in the
	// values a request needs that the environment already has: the IDs of
	// existing items and DaVinci variable values. sourceEnvironmentIds are
	// included.
	EnvironmentIDs map[string]string `json:"environmentIds,omitempty"`
	Rollout        []string          `json:"rollout"`
	BranchPrefix   string            `json:"branchPrefix,omitempty"`
	// BranchPattern, when set, makes the person type the branch name (for
	// example a Jira key) and is the rule it must match whole, such as
	// CIAM-[0-9]+. BranchExample is shown as the example.
	BranchPattern string `json:"branchPattern,omitempty"`
	BranchExample string `json:"branchExample,omitempty"`
	branchRE      *regexp.Regexp
	// warnings are configuration problems that do not stop the server.
	warnings       []string
	OverlayPattern string `json:"overlayPattern,omitempty"`
	// Layout is "default" or "terraformer" (file names of the root).
	Layout string `json:"layout,omitempty"`
	// Repository adjusts the layout to the config repository: file and
	// module names, the variables the pipeline supplies, and where new
	// secrets live in Vault. Every field is optional.
	Repository RepositoryConfig `json:"repository,omitempty"`
	// ApplyJobPattern names each environment's apply job in the pipeline
	// of the default branch, with {env}; the Requests page reads it.
	ApplyJobPattern string `json:"applyJobPattern,omitempty"`

	Tools struct {
		Terraform          string `json:"terraform"`
		TerraformVersion   string `json:"terraformVersion,omitempty"`
		TerraformSHA256    string `json:"terraformSha256"`
		PingCLI            string `json:"pingcli,omitempty"`
		PingCLIVersion     string `json:"pingcliVersion,omitempty"`
		PingCLISHA256      string `json:"pingcliSha256,omitempty"`
		Terraformer        string `json:"terraformer"`
		TerraformerVersion string `json:"terraformerVersion"`
		// TerraformerSHA256 pins the executable used by the hosted server.
		TerraformerSHA256 string `json:"terraformerSha256,omitempty"`
	} `json:"tools"`
}

var (
	envNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	hostRE    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,253}[A-Za-z0-9])?$`)
	envIDRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	jobPatRE  = regexp.MustCompile(`^[A-Za-z0-9 :._/-]{0,80}\{env\}[A-Za-z0-9 :._/-]{0,80}$`)
)

// configDecodeError says where the configuration file is wrong: the line of
// a syntax error, the name of an unknown or mistyped setting. It never
// repeats a value from the file.
func configDecodeError(raw []byte, err error) error {
	line := func(off int64) int {
		if off > int64(len(raw)) {
			off = int64(len(raw))
		}
		return 1 + strings.Count(string(raw[:off]), "\n")
	}
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Errorf("configuration file is not valid JSON near line %d (look for a missing comma or quote, or a comma after the last item)", line(syn.Offset))
	case errors.As(err, &typ):
		return fmt.Errorf("configuration setting %q near line %d has the wrong kind of value (expected %s)", typ.Field, line(typ.Offset), typ.Type.String())
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errors.New("configuration file ends early (a missing closing brace or bracket?)")
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return fmt.Errorf("configuration file contains the unknown setting %s; check its spelling and where it sits (docs/podman-mac.md, step 5, lists every setting)", name)
	}
	return errors.New("configuration file is malformed")
}

// LoadConfig reads and checks the configuration file.
func LoadConfig(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("cannot read the configuration file %s", path)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, configDecodeError(raw, err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return c, errors.New("configuration file must contain one JSON object")
	}
	return c, c.check()
}

func (c *Config) check() error {
	if c.Tools.TerraformVersion == "" {
		c.Tools.TerraformVersion = "1.16.7"
	}
	if c.Tools.TerraformVersion != "1.16.7" {
		return errors.New("tools.terraformVersion must be 1.16.7")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must be host:port, for example 127.0.0.1:8080")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("set both tlsCertFile and tlsKeyFile, or neither")
	}
	if err := c.checkExposure(); err != nil {
		return err
	}
	switch c.SignIn {
	case "", "none":
		c.SignIn = "none"
	case "pingone":
		return errors.New(`signIn "pingone" is not available yet; use "none"`)
	default:
		return errors.New(`signIn must be "none"`)
	}
	for _, h := range c.AllowedHosts {
		if !hostRE.MatchString(h) {
			return fmt.Errorf("allowedHosts: %q is not a hostname", h)
		}
	}
	if c.SecretsFile == "" {
		return errors.New("secretsFile is required")
	}
	if err := (gitlab.Config{BaseURL: c.GitLab.BaseURL, ProjectID: c.GitLab.ProjectID, TokenEnv: "T"}).Validate(); err != nil {
		return fmt.Errorf("gitlab: %w", err)
	}
	if c.PingOne.RegionCode == "" || c.PingOne.WorkerEnvironmentID == "" {
		return errors.New("pingone.regionCode and pingone.workerEnvironmentId are required")
	}
	if len(c.Sources) == 0 || len(c.Rollout) == 0 {
		return errors.New("sources and rollout are required, for example [\"development\"] and [\"testing\", \"production\"]")
	}
	seen := map[string]bool{}
	for _, e := range append(append([]string{}, c.Sources...), c.Rollout...) {
		if !envNameRE.MatchString(e) {
			return fmt.Errorf("%q is not an environment name", e)
		}
		if seen[e] {
			return fmt.Errorf("environment %q is listed twice; a source cannot also be in the rollout", e)
		}
		seen[e] = true
	}
	for name, id := range c.SourceEnvironmentIDs {
		if !seen[name] || !contains(c.Sources, name) {
			return fmt.Errorf("sourceEnvironmentIds: %q is not one of the sources", name)
		}
		if !envIDRE.MatchString(id) {
			return fmt.Errorf("sourceEnvironmentIds.%s must be a PingOne environment ID (a UUID)", name)
		}
	}
	for _, name := range sortedNames(c.EnvironmentIDs) {
		id := c.EnvironmentIDs[name]
		if !seen[name] {
			// An extra environment (not copied from or rolled out to)
			// is only of no use: noted, not fatal.
			c.warnings = append(c.warnings, fmt.Sprintf("environmentIds: %q is neither in sources nor in rollout, so it is not used; check the name matches the .tfvars file (%s.tfvars)", name, name))
			delete(c.EnvironmentIDs, name)
			continue
		}
		if !envIDRE.MatchString(id) {
			return fmt.Errorf("environmentIds.%s must be a PingOne environment ID (a UUID)", name)
		}
		if other, ok := c.SourceEnvironmentIDs[name]; ok && !strings.EqualFold(other, id) {
			return fmt.Errorf("environmentIds.%s differs from sourceEnvironmentIds.%s", name, name)
		}
		c.EnvironmentIDs[name] = strings.ToLower(id)
	}
	byID := map[string]string{}
	for _, m := range []map[string]string{c.SourceEnvironmentIDs, c.EnvironmentIDs} {
		for _, name := range sortedNames(m) {
			m[name] = strings.ToLower(m[name])
			if other, ok := byID[m[name]]; ok && other != name {
				return fmt.Errorf("environmentIds: %s and %s have the same PingOne environment ID; each environment has its own (a copy-paste mistake here would point one at the other)", other, name)
			}
			byID[m[name]] = name
		}
	}

	if c.BranchPrefix == "" {
		c.BranchPrefix = gitlab.RequestBranchPrefix
	}
	if !gitlab.ValidRequestBranchPrefix(c.BranchPrefix) {
		return errors.New("branchPrefix must start with a letter or digit and end with / or -, such as CIAM-")
	}
	if c.BranchPattern != "" {
		if len(c.BranchPattern) > 200 {
			return errors.New("branchPattern is too long")
		}
		re, err := compileBranchPattern(c.BranchPattern)
		if err != nil {
			return err
		}
		if c.BranchExample != "" && !re.MatchString(c.BranchExample) {
			return errors.New("branchExample does not match branchPattern")
		}
		c.branchRE = re
	}
	if c.ApplyJobPattern == "" {
		c.ApplyJobPattern = "apply:{env}"
	}
	if !jobPatRE.MatchString(c.ApplyJobPattern) {
		return errors.New("applyJobPattern must contain {env}, such as apply:{env}")
	}
	if c.Tools.Terraform == "" || c.Tools.Terraformer == "" || c.Tools.TerraformerVersion == "" {
		return errors.New("tools.terraform, tools.terraformer and tools.terraformerVersion are required")
	}
	digestRE := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !digestRE.MatchString(c.Tools.TerraformSHA256) || !digestRE.MatchString(c.Tools.TerraformerSHA256) {
		return errors.New("tools.terraformSha256 and tools.terraformerSha256 must be pinned SHA-256 digests")
	}
	if c.Tools.PingCLI != "" && (c.Tools.PingCLIVersion == "" || !digestRE.MatchString(c.Tools.PingCLISHA256)) {
		return errors.New("pingcli requires a pinned version and SHA-256")
	}
	return nil
}

// Exposure values.
const (
	ExposureTLS         = "tls"
	ExposureBehindProxy = "behind-proxy"
	// ExposureContainer is for a container whose port is published only
	// to its host's loopback (podman run -p 127.0.0.1:8080:8080): the
	// server listens on every interface inside the container, speaks plain
	// HTTP, and answers only the loopback names (localhost, 127.0.0.1, ::1).
	ExposureContainer = "container"
)

// LoopbackListen reports whether listen binds only a loopback address.
func LoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkExposure refuses a non-loopback listen address without an explicit
// opt-in that names how the traffic is protected.
func (c *Config) checkExposure() error {
	switch c.Exposure {
	case "":
		if !LoopbackListen(c.Listen) {
			return errors.New(`listen is not a loopback address; there is no sign-in, so set "exposure": "tls" (with tlsCertFile and tlsKeyFile), "behind-proxy" (with allowedHosts), or "container" (a container published only to its host's 127.0.0.1) to expose the server deliberately, or listen on 127.0.0.1`)
		}
	case ExposureTLS:
		if c.TLSCertFile == "" {
			return errors.New(`"exposure": "tls" requires tlsCertFile and tlsKeyFile`)
		}
	case ExposureBehindProxy:
		if len(c.AllowedHosts) == 0 {
			return errors.New(`"exposure": "behind-proxy" requires allowedHosts (the name the proxy serves)`)
		}
	case ExposureContainer:
		if len(c.AllowedHosts) > 0 {
			return errors.New(`"exposure": "container" answers only localhost; remove allowedHosts, or use "behind-proxy" to serve a host name`)
		}
		if c.TLSCertFile != "" {
			return errors.New(`"exposure": "container" is plain HTTP on the host's loopback; use "tls" to terminate TLS`)
		}
	default:
		return errors.New(`exposure must be "tls", "behind-proxy" or "container" (or empty for loopback only)`)
	}
	return nil
}

// Secrets are read from the sensitive file, never from the configuration.
type Secrets struct {
	WorkerClientID     string
	WorkerClientSecret string
	GitLabToken        string
}

// Secret names in the file.
const (
	SecretWorkerClientID     = "PINGONE_PROMOTE_WORKER_CLIENT_ID"
	SecretWorkerClientSecret = "PINGONE_PROMOTE_WORKER_CLIENT_SECRET"
	SecretGitLabToken        = "PINGONE_PROMOTE_GITLAB_TOKEN"
)

// secretsMax bounds the secrets file.
const secretsMax = 64 << 10

// LoadSecrets reads NAME=value lines (blank lines and # comments allowed).
//
// The file must be a regular file (not a symbolic link) owned by the user
// the server runs as, with mode 0600 or 0400 exactly: no group or other
// permission bit. In a pod, mount the secret with defaultMode 0400 and set
// runAsUser/fsGroup so the file's owner is the server's user. Errors name
// line numbers and the expected names, never a value or an unknown name.
func LoadSecrets(path string) (Secrets, error) {
	var s Secrets
	li, err := os.Lstat(path)
	if err != nil {
		return s, errors.New("cannot open the secrets file")
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return s, errors.New("the secrets file must not be a symbolic link; point secretsFile at the file itself")
	}
	if !li.Mode().IsRegular() {
		return s, errors.New("the secrets file must be a regular file")
	}
	if err := checkSecretsFileInfo(path, li); err != nil {
		return s, err
	}
	// O_NOFOLLOW refuses a symbolic link swapped in after the Lstat, and
	// O_NONBLOCK keeps a FIFO from blocking the open; SameFile below then
	// proves the opened file is the one checked.
	f, err := openSecretsFile(path)
	if err != nil {
		return s, errors.New("cannot open the secrets file (it must be a regular file, not a symbolic link)")
	}
	defer f.Close()
	fi, err := f.Stat()
	// The file opened must be the one checked: refuse a swap between the
	// two calls.
	if err != nil || !os.SameFile(li, fi) || !fi.Mode().IsRegular() {
		return s, errors.New("the secrets file changed while it was opened; try again")
	}
	if err := checkSecretsFileInfo(path, fi); err != nil {
		return s, err
	}
	if fi.Size() > secretsMax {
		return s, errors.New("the secrets file exceeds 64 KiB")
	}
	return parseSecrets(io.LimitReader(f, secretsMax+1))
}

// checkSecretsMode accepts exactly 0600 or 0400.
func checkSecretsMode(mode os.FileMode) error {
	if perm := mode.Perm(); perm != 0o600 && perm != 0o400 {
		return errors.New("the secrets file must be readable by its owner only (mode 0600 or 0400, no group or other access); run chmod 600 on it")
	}
	return nil
}

// parseSecrets parses the NAME=value content. A value may contain '='; only
// the first one separates the name.
func parseSecrets(r io.Reader) (Secrets, error) {
	var s Secrets
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), secretsMax+1)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if !ok || k == "" || v == "" {
			return Secrets{}, fmt.Errorf("secrets file line %d: expected NAME=value", n)
		}
		if seen[k] {
			return Secrets{}, fmt.Errorf("secrets file line %d: a name is set twice", n)
		}
		seen[k] = true
		switch k {
		case SecretWorkerClientID:
			s.WorkerClientID = v
		case SecretWorkerClientSecret:
			s.WorkerClientSecret = v
		case SecretGitLabToken:
			s.GitLabToken = v
		default:
			return Secrets{}, fmt.Errorf("secrets file line %d: unknown name; expected %s, %s or %s", n, SecretWorkerClientID, SecretWorkerClientSecret, SecretGitLabToken)
		}
	}
	if err := sc.Err(); err != nil {
		return Secrets{}, errors.New("cannot read the secrets file")
	}
	if s.WorkerClientID == "" || s.WorkerClientSecret == "" || s.GitLabToken == "" {
		return Secrets{}, fmt.Errorf("the secrets file must set %s, %s and %s", SecretWorkerClientID, SecretWorkerClientSecret, SecretGitLabToken)
	}
	return s, nil
}

// branchRE is the compiled branch pattern (nil without one), compiled by
// Load and New.
func (s *Server) branchRE() *regexp.Regexp { return s.cfg.branchRE }

// RepositoryConfig describes the config repository where it differs from
// the default layout.
type RepositoryConfig struct {
	ModuleName          string `json:"moduleName,omitempty"`
	ModuleDir           string `json:"moduleDir,omitempty"`
	ModuleFile          string `json:"moduleFile,omitempty"`
	VariablesFile       string `json:"variablesFile,omitempty"`
	SecretsFile         string `json:"secretsFile,omitempty"`
	ImportsFile         string `json:"importsFile,omitempty"`
	CustomerSecretsFile string `json:"customerSecretsFile,omitempty"`
	// CIVariables are root variables the pipeline supplies; they are never
	// asked for or written to a tfvars file. pingone_environment_id is
	// added when sourceEnvironmentIds is set.
	CIVariables []string `json:"ciVariables,omitempty"`
	// VaultPathPrefix and VaultMount place new secrets in Vault:
	// <prefix>/<name>@<mount>.
	VaultPathPrefix string `json:"vaultPathPrefix,omitempty"`
	VaultMount      string `json:"vaultMount,omitempty"`
}

// compileBranchPattern accepts a pattern only if it is a valid regular
// expression on its own, so it cannot escape the anchors around it (for
// example "X)|(.*").
func compileBranchPattern(p string) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(p); err != nil {
		return nil, fmt.Errorf("branchPattern is not a valid regular expression: %v", err)
	}
	re, err := regexp.Compile(`^(?:` + p + `)$`)
	if err != nil {
		return nil, fmt.Errorf("branchPattern is not a valid regular expression: %v", err)
	}
	return re, nil
}

// envID is an environment's configured PingOne ID (environmentIds, or
// sourceEnvironmentIds for a source), or "".
func (c *Config) envID(name string) string {
	if id := c.EnvironmentIDs[name]; id != "" {
		return id
	}
	return c.SourceEnvironmentIDs[name]
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
