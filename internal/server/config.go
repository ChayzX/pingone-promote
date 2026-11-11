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
	"strings"

	"pingone-promote/internal/gitlab"
)

// Config is the non-secret configuration file (JSON).
type Config struct {
	// Listen is host:port. The default 127.0.0.1:8080 is reachable from
	// this machine only.
	Listen string `json:"listen"`
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
	Sources        []string `json:"sources"`
	Rollout        []string `json:"rollout"`
	BranchPrefix   string   `json:"branchPrefix,omitempty"`
	OverlayPattern string   `json:"overlayPattern,omitempty"`
	// Layout is "default" or "terraformer" (file names of the root).
	Layout string `json:"layout,omitempty"`
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
	jobPatRE  = regexp.MustCompile(`^[A-Za-z0-9 :._/-]{0,80}\{env\}[A-Za-z0-9 :._/-]{0,80}$`)
)

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
		return c, errors.New("configuration file is malformed or contains an unknown field")
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
	if c.BranchPrefix == "" {
		c.BranchPrefix = gitlab.RequestBranchPrefix
	}
	if !gitlab.ValidRequestBranchPrefix(c.BranchPrefix) {
		return errors.New("branchPrefix must start with a letter or digit and end with / or -, such as CIAM-")
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

// LoadSecrets reads NAME=value lines (blank lines and # comments allowed).
// The file must be a regular file that other users cannot read and that
// the group cannot access: mode 0600 or 0400. Values are never logged.
func LoadSecrets(path string) (Secrets, error) {
	var s Secrets
	f, err := os.Open(path)
	if err != nil {
		return s, errors.New("cannot open the secrets file")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return s, errors.New("the secrets file must be a regular file")
	}
	if mode := fi.Mode().Perm(); mode != 0600 && mode != 0400 {
		return s, errors.New("the secrets file must have mode 0600 or 0400; run chmod 600 on it")
	}
	if fi.Size() > 64<<10 {
		return s, errors.New("the secrets file exceeds 64 KiB")
	}
	seen := map[string]bool{}
	sc := bufio.NewScanner(io.LimitReader(f, (64<<10)+1))
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
		if !ok || v == "" {
			return s, fmt.Errorf("secrets file line %d: expected NAME=value", n)
		}
		if seen[k] {
			return Secrets{}, fmt.Errorf("secrets file line %d: duplicate name", n)
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
			return Secrets{}, fmt.Errorf("secrets file line %d: unknown name", n)
		}
	}
	if err := sc.Err(); err != nil {
		return s, errors.New("cannot read the secrets file")
	}
	if s.WorkerClientID == "" || s.WorkerClientSecret == "" || s.GitLabToken == "" {
		return s, fmt.Errorf("the secrets file must set %s, %s and %s", SecretWorkerClientID, SecretWorkerClientSecret, SecretGitLabToken)
	}
	return s, nil
}
