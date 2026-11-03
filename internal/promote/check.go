package promote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/secretscan"
	"pingone-promote/internal/tfvars"
)

// CheckOptions configure CheckRoot.
type CheckOptions struct {
	Root   string
	Layout Layout
	// Envs are the environments that must have complete overlays (source
	// and rollout). Overlays found on disk are always checked too.
	Envs []string
	// CustomerSecrets is the customer_secrets.yml content, nil to skip.
	CustomerSecrets []byte
	SecretsConfig   *pipelinesecrets.Config
	// TerraformFmt pipes every Terraform file through `terraform fmt -`.
	TerraformFmt bool
	// TerraformValidate runs `terraform init -backend=false` and
	// `terraform validate` on a private copy of the root.
	TerraformValidate bool
	// TerraformBin is the terraform (or tofu) binary; "" searches PATH.
	TerraformBin string
	// PluginDir is passed to init as -plugin-dir (offline provider mirror).
	PluginDir string
}

// Problem is one check failure or warning.
type Problem struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Env     string `json:"env,omitempty"`
	Message string `json:"message"`
}

// CheckReport is the result of CheckRoot.
type CheckReport struct {
	OK                bool                    `json:"ok"`
	Root              string                  `json:"root"`
	Envs              []string                `json:"envs"`
	Problems          []Problem               `json:"problems"`
	Warnings          []Problem               `json:"warnings"`
	RequiredSecrets   []string                `json:"requiredSecrets"`
	TerraformFmt      string                  `json:"terraformFmt,omitempty"`
	TerraformValidate string                  `json:"terraformValidate,omitempty"`
	CustomerSecrets   *pipelinesecrets.Report `json:"customerSecrets,omitempty"`
}

// ErrToolMissing means a requested terraform step had no binary.
var ErrToolMissing = errors.New("terraform binary not found")

// CheckRoot validates a config repository root offline.
func CheckRoot(ctx context.Context, o CheckOptions) (*CheckReport, error) {
	l := o.Layout.WithDefaults()
	if err := l.Validate(); err != nil {
		return nil, err
	}
	repo := DirRepo(o.Root)
	rep := &CheckReport{Root: o.Root, Problems: []Problem{}, Warnings: []Problem{}, RequiredSecrets: []string{}}
	add := func(code, p, env, msg string) { rep.Problems = append(rep.Problems, Problem{Code: code, Path: p, Env: env, Message: msg}) }
	ix, err := loadIndex(repo, l)
	if err != nil {
		add("parse", "", "", err.Error())
		rep.finish()
		return rep, nil
	}
	if ix.moduleFile == "" {
		add("module_missing", l.ModuleFile, "", "no module \""+l.ModuleName+"\" block at the root")
	}
	files, err := tfFiles(repo, l)
	if err != nil {
		return nil, err
	}
	// Files that must not exist.
	for _, p := range files.all {
		if strings.HasSuffix(p, ".auto.tfvars") || strings.HasSuffix(p, ".auto.tfvars.json") {
			add(BlockAutoTfvars, p, "", "*.auto.tfvars is loaded automatically and would override pipeline TF_VAR_ secrets; delete it")
		}
	}
	// Root import blocks must be conditional (ADR 0016).
	for _, p := range ix.rootTF {
		src, _, _ := repo.Read(p)
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			continue
		}
		for _, b := range body.Blocks {
			if b.Type != "import" {
				continue
			}
			if _, ok := b.Body.Attributes["for_each"]; !ok {
				add("literal_import", p, "", fmt.Sprintf("line %d: import blocks must be driven by var.import_ids (for_each); literal IDs are per environment", b.TypeRange.Start.Line))
			}
		}
	}
	// Overlays.
	envs := append([]string(nil), o.Envs...)
	found, err := overlayFiles(repo, l)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range envs {
		seen[e] = true
	}
	for e := range found {
		if !seen[e] {
			envs = append(envs, e)
			seen[e] = true
		}
	}
	sort.Strings(envs)
	rep.Envs = envs
	if len(envs) == 0 {
		add(BlockMissingOverlay, "", "", "no environment overlays found ("+l.OverlayPattern+")")
	}
	blockers, err := ValidateOverlays(repo, l, envs)
	if err != nil {
		return nil, err
	}
	for _, b := range blockers {
		p, _ := l.Overlay(b.Env)
		add(b.Code, p, b.Env, b.Message)
	}
	// Secrets.
	sec := ix.secretVars()
	for n := range sec {
		if ix.moduleInputs[n] {
			rep.RequiredSecrets = append(rep.RequiredSecrets, n)
		}
	}
	sort.Strings(rep.RequiredSecrets)
	if o.CustomerSecrets != nil {
		pf, err := pipelinesecrets.Parse(o.CustomerSecrets)
		if err != nil {
			add("customer_secrets", l.CustomerSecretsFile, "", err.Error())
		} else {
			cfg := pipelinesecrets.DefaultConfig
			if o.SecretsConfig != nil {
				cfg = *o.SecretsConfig
			}
			r := pf.Check(cfg, rep.RequiredSecrets)
			rep.CustomerSecrets = &r
			for _, k := range r.Undeclared {
				add("secret_undeclared", l.CustomerSecretsFile, "", "secret "+k+" is not declared; add TF_VAR_"+k)
			}
			for _, k := range r.CommentedOut {
				add("secret_commented_out", l.CustomerSecretsFile, "", "secret "+k+" is declared only in a comment; uncomment TF_VAR_"+k)
			}
			for _, k := range r.Unused {
				rep.Warnings = append(rep.Warnings, Problem{Code: "secret_unused", Path: l.CustomerSecretsFile, Message: "TF_VAR_" + k + " is declared but not used by this root"})
			}
			for _, k := range r.PatternMismatch {
				rep.Warnings = append(rep.Warnings, Problem{Code: "secret_path_unusual", Path: l.CustomerSecretsFile, Message: "TF_VAR_" + k + " is not at " + cfg.PathPrefix + "/<field>@" + cfg.Mount})
			}
		}
	}
	// Secret and masked-value scan over every Terraform file.
	for _, p := range files.all {
		src, _, err := repo.Read(p)
		if err != nil {
			return nil, err
		}
		for _, f := range secretscan.ScanText(p, src) {
			add(BlockSecretFinding, p, "", fmt.Sprintf("line %d: looks like a secret (%s)", f.Line, f.Kind))
		}
	}
	if o.TerraformFmt {
		rep.TerraformFmt = "ok"
		for _, p := range files.formattable {
			src, _, _ := repo.Read(p)
			if _, err := tfvars.FormatWithTerraform(ctx, o.TerraformBin, src); err != nil {
				var nf *tfvars.FormatterNotFoundError
				if errors.As(err, &nf) {
					return nil, ErrToolMissing
				}
				rep.TerraformFmt = "differs"
				add("terraform_fmt", p, "", err.Error())
			}
		}
	}
	if o.TerraformValidate {
		out, err := terraformValidate(ctx, o)
		switch {
		case errors.Is(err, ErrToolMissing):
			return nil, err
		case err != nil:
			rep.TerraformValidate = "failed"
			add("terraform_validate", "", "", err.Error()+"\n"+out)
		default:
			rep.TerraformValidate = "ok"
		}
	}
	rep.finish()
	return rep, nil
}

func (r *CheckReport) finish() {
	sort.SliceStable(r.Problems, func(i, j int) bool {
		if r.Problems[i].Path != r.Problems[j].Path {
			return r.Problems[i].Path < r.Problems[j].Path
		}
		return r.Problems[i].Code < r.Problems[j].Code
	})
	r.OK = len(r.Problems) == 0
}

type tfSet struct{ all, formattable []string }

func tfFiles(r Repo, l Layout) (tfSet, error) {
	var s tfSet
	dirs := []string{".", l.ModuleDir}
	if od := path.Dir(strings.ReplaceAll(l.OverlayPattern, "{env}", "x")); od != "." {
		dirs = append(dirs, od)
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		list, err := r.List(d)
		if err != nil {
			return s, err
		}
		for _, p := range list {
			if seen[p] {
				continue
			}
			seen[p] = true
			switch {
			case strings.HasSuffix(p, ".tf"), strings.HasSuffix(p, ".tfvars"):
				s.all = append(s.all, p)
				s.formattable = append(s.formattable, p)
			case strings.HasSuffix(p, ".tfvars.json"), strings.HasSuffix(p, ".tf.json"):
				s.all = append(s.all, p)
			}
		}
	}
	sort.Strings(s.all)
	sort.Strings(s.formattable)
	return s, nil
}

// terraformValidate copies the root's Terraform files to a private
// directory and runs init (no backend) and validate there, so the working
// tree is never modified.
func terraformValidate(ctx context.Context, o CheckOptions) (string, error) {
	bin, err := tfvars.ResolveFormatter(o.TerraformBin)
	if err != nil {
		return "", ErrToolMissing
	}
	tmp, err := os.MkdirTemp("", "pingone-promote-validate-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	l := o.Layout.WithDefaults()
	for _, d := range []string{".", l.ModuleDir} {
		list, err := DirRepo(o.Root).List(d)
		if err != nil {
			return "", err
		}
		for _, p := range list {
			if !strings.HasSuffix(p, ".tf") && !strings.HasSuffix(p, ".tf.json") && path.Base(p) != ".terraform.lock.hcl" {
				continue
			}
			if d == "." && strings.HasPrefix(path.Base(p), "backend") {
				continue // backends need credentials; -backend=false skips them anyway
			}
			src, _, err := DirRepo(o.Root).Read(p)
			if err != nil {
				return "", err
			}
			dst := filepath.Join(tmp, filepath.FromSlash(p))
			os.MkdirAll(filepath.Dir(dst), 0o700)
			if err := os.WriteFile(dst, src, 0o600); err != nil {
				return "", err
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = tmp
		cmd.Env = validateEnv(tmp)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	initArgs := []string{"init", "-backend=false", "-input=false", "-no-color"}
	if o.PluginDir != "" {
		initArgs = append(initArgs, "-plugin-dir="+o.PluginDir)
	}
	if out, err := run(initArgs...); err != nil {
		return lastLines(out, 30), errors.New("terraform init failed")
	}
	if out, err := run("validate", "-no-color"); err != nil {
		return lastLines(out, 30), errors.New("terraform validate failed")
	}
	return "", nil
}

// validateEnv passes only what init needs to reach a registry or mirror.
func validateEnv(home string) []string {
	env := []string{"HOME=" + home, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "TF_CLI_CONFIG_FILE", "TF_PLUGIN_CACHE_DIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
