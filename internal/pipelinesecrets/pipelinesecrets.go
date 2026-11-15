// Package pipelinesecrets reads and extends the enterprise pipeline's
// customer_secrets.yml: GitLab CI's secrets: keyword inside a hidden
// .customer-secrets job, which other jobs extend:
//
//	.customer-secrets:
//	  secrets:
//	    TF_VAR_client_secret:
//	      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/client_secret@secret
//	      file: false
//
// Each entry becomes a TF_VAR_<name> environment variable. The vault value
// uses GitLab's short syntax <path>/<field>@<mount>. ${VAULT_PATH_ENV}
// selects the environment, so one file serves every environment.
//
// The file is edited by inserting lines, never by re-encoding: yaml.v3 is used
// only to parse and locate entries, because re-encoding reflows indentation,
// blank lines and comments. Existing entries are never removed or rewritten,
// and commented-out entries are never edited. A file with no changes
// round-trips byte for byte.
package pipelinesecrets

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the fixed location of PingOne secrets in Vault: only the field
// changes per secret.
type Config struct {
	PathPrefix string // without a trailing slash
	Mount      string
}

// DefaultConfig is where every PingOne secret lives.
var DefaultConfig = Config{PathPrefix: "apps/pingone-davinci/${VAULT_PATH_ENV}/pingone", Mount: "secret"}

// SecretNamePattern is the rule for secret variable names (ADR 0009); it
// equals tfvars.SecretKeyPattern and is kept here so this package does not
// depend on the v1 tfvars schema.
const SecretNamePattern = `^[A-Za-z][A-Za-z0-9_]{0,127}$`

var secretNameRE = regexp.MustCompile(SecretNamePattern)

// EnvPrefix is the GitLab variable prefix Terraform reads.
const EnvPrefix = "TF_VAR_"

// MaxFileSize bounds the input.
const MaxFileSize = 4 << 20

var (
	prefixRE    = regexp.MustCompile(`^[A-Za-z0-9_.${}/-]+$`)
	mountRE     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	fieldRE     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	commentedRE = regexp.MustCompile(`^\s*#\s*(` + EnvPrefix + `[A-Za-z0-9_]+)\s*:`)
)

// Entry is one declared secret.
type Entry struct {
	EnvVar    string `json:"env_var"`  // as written, e.g. TF_VAR_client_secret
	VarName   string `json:"var_name"` // EnvVar without TF_VAR_; empty for non-Terraform entries
	VaultPath string `json:"vault_path"`
	Field     string `json:"field"`
	Mount     string `json:"mount"`
	File      *bool  `json:"file,omitempty"`
	Line      int    `json:"line"`
	ShortForm bool   `json:"short_form"` // vault: is a "<path>/<field>@<mount>" string
}

// File is a parsed customer_secrets.yml.
type File struct {
	src       []byte
	lines     []string
	newline   string
	entries   []Entry
	commented map[string]int // env var -> line
	// Layout of the secrets mapping, for insertion.
	secretsLine  int // 1-based line of "secrets:"
	entryIndent  int
	childIndent  int
	blankBetween bool
	insertAfter  int // 1-based line after which new entries go
	emptyMapping bool
}

// Parse reads src. It requires a top-level .customer-secrets mapping with a
// block-style secrets mapping (which may be empty).
func Parse(src []byte) (*File, error) {
	if len(src) > MaxFileSize {
		return nil, errors.New("customer_secrets.yml exceeds the size limit")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, errors.New("customer_secrets.yml is not valid YAML")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("customer_secrets.yml must be a YAML mapping")
	}
	f := &File{src: src, newline: "\n", commented: map[string]int{}}
	if bytes.Contains(src, []byte("\r\n")) {
		f.newline = "\r\n"
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	f.lines = strings.Split(text, "\n")

	job := mappingValue(root.Content[0], ".customer-secrets")
	if job == nil || job.Kind != yaml.MappingNode {
		return nil, errors.New("customer_secrets.yml has no .customer-secrets mapping")
	}
	secretsKey, secrets := mappingEntry(job, "secrets")
	if secretsKey == nil {
		return nil, errors.New(".customer-secrets has no secrets: key")
	}
	f.secretsLine = secretsKey.Line
	switch {
	case secrets.Kind == yaml.ScalarNode && secrets.Tag == "!!null":
		f.emptyMapping = true
	case secrets.Kind != yaml.MappingNode:
		return nil, errors.New(".customer-secrets.secrets must be a mapping")
	case secrets.Style&yaml.FlowStyle != 0:
		return nil, errors.New(".customer-secrets.secrets uses flow style; convert it to block style to let pingone-promote add entries")
	case len(secrets.Content) == 0:
		f.emptyMapping = true
	}
	f.entryIndent, f.childIndent = secretsKey.Column-1+2, secretsKey.Column-1+4
	seen := map[string]bool{}
	for i := 0; !f.emptyMapping && i+1 < len(secrets.Content); i += 2 {
		k, v := secrets.Content[i], secrets.Content[i+1]
		if seen[k.Value] {
			return nil, fmt.Errorf("line %d: duplicate secret %q", k.Line, k.Value)
		}
		seen[k.Value] = true
		e := Entry{EnvVar: k.Value, Line: k.Line}
		if name, ok := strings.CutPrefix(k.Value, EnvPrefix); ok {
			e.VarName = name
		}
		if i == 0 {
			f.entryIndent = k.Column - 1
			if v.Kind == yaml.MappingNode && len(v.Content) > 0 {
				f.childIndent = v.Content[0].Column - 1
			} else {
				f.childIndent = f.entryIndent + 2
			}
		}
		if i == 2 {
			f.blankBetween = k.Line >= 2 && strings.TrimSpace(f.lines[k.Line-2]) == ""
		}
		if v.Kind == yaml.MappingNode {
			if vault := mappingValue(v, "vault"); vault != nil && vault.Kind == yaml.ScalarNode {
				e.VaultPath, e.Field, e.Mount = SplitVault(vault.Value)
				e.ShortForm = true
			}
			if fv := mappingValue(v, "file"); fv != nil && fv.Kind == yaml.ScalarNode {
				b := fv.Value == "true"
				e.File = &b
			}
		}
		f.entries = append(f.entries, e)
	}
	if len(f.entries) == 1 {
		f.blankBetween = true // the enterprise style; nothing to infer from
	}
	f.locate(secretsKey.Column - 1)
	return f, nil
}

// locate finds where new entries go (right after the last real entry) and
// records commented-out entries inside the secrets block.
func (f *File) locate(secretsIndent int) {
	f.insertAfter = f.secretsLine
	for ln := f.secretsLine + 1; ln <= len(f.lines); ln++ {
		line := f.lines[ln-1]
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if m := commentedRE.FindStringSubmatch(line); m != nil && indent > secretsIndent {
				f.commented[m[1]] = ln
			}
			continue
		}
		if indent <= secretsIndent {
			break // next key of .customer-secrets, or a later top-level key
		}
		f.insertAfter = ln
	}
}

func mappingEntry(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	_, v := mappingEntry(m, key)
	return v
}

// SplitVault splits GitLab's short syntax <path>/<field>@<mount> on the last
// "@" and then the last "/". ${VAULT_PATH_ENV} stays literal.
func SplitVault(s string) (path, field, mount string) {
	rest := s
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest, mount = rest[:i], rest[i+1:]
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		return rest[:i], rest[i+1:], mount
	}
	return "", rest, mount
}

// Entries returns the declared entries in file order.
func (f *File) Entries() []Entry { return append([]Entry(nil), f.entries...) }

// Lookup returns the declared entry whose TF_VAR_ name is key.
func (f *File) Lookup(key string) (Entry, bool) {
	for _, e := range f.entries {
		if e.VarName == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Commented lists commented-out TF_VAR_ entries, sorted.
func (f *File) Commented() []string {
	out := make([]string, 0, len(f.commented))
	for k := range f.commented {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Report compares required secret keys with the declared entries.
type Report struct {
	Declared        []string `json:"declared"`         // required and declared
	Undeclared      []string `json:"undeclared"`       // required, not declared (fails check)
	CommentedOut    []string `json:"commented_out"`    // required, present only as a comment (fails check)
	Unused          []string `json:"unused"`           // declared TF_VAR_ names nothing requires (warning)
	PatternMismatch []string `json:"pattern_mismatch"` // declared names not at <prefix>/<field>@<mount> (warning)
	Candidates      []string `json:"candidates"`       // every declared TF_VAR_ name, for mapping a key to an existing entry
}

// OK reports whether every required secret is declared.
func (r Report) OK() bool { return len(r.Undeclared) == 0 && len(r.CommentedOut) == 0 }

// Check compares the file with the secret keys a tfvars document requires.
func (f *File) Check(cfg Config, required []string) Report {
	r := Report{Declared: []string{}, Undeclared: []string{}, CommentedOut: []string{}, Unused: []string{}, PatternMismatch: []string{}, Candidates: []string{}}
	need := map[string]bool{}
	for _, k := range required {
		need[k] = true
	}
	for _, e := range f.entries {
		if e.VarName == "" {
			continue
		}
		r.Candidates = append(r.Candidates, e.VarName)
		if need[e.VarName] {
			r.Declared = append(r.Declared, e.VarName)
		} else {
			r.Unused = append(r.Unused, e.VarName)
		}
		if !e.ShortForm || e.VaultPath != cfg.PathPrefix || e.Mount != cfg.Mount {
			r.PatternMismatch = append(r.PatternMismatch, e.VarName)
		}
	}
	for _, k := range sortedSet(need) {
		if _, ok := f.Lookup(k); ok {
			continue
		}
		if _, ok := f.commented[EnvPrefix+k]; ok {
			r.CommentedOut = append(r.CommentedOut, k)
		} else {
			r.Undeclared = append(r.Undeclared, k)
		}
	}
	for _, s := range []*[]string{&r.Declared, &r.Unused, &r.PatternMismatch, &r.Candidates} {
		sort.Strings(*s)
	}
	return r
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NewEntry is a secret to declare. Field defaults to VarName.
type NewEntry struct {
	VarName string
	Field   string
}

// EditError reports why an entry cannot be added.
type EditError struct {
	VarName string
	Reason  string
}

func (e *EditError) Error() string { return "customer_secrets.yml: " + e.VarName + ": " + e.Reason }

// Add returns src with the new entries appended to the end of the secrets
// mapping, in the order given, using the file's indentation, blank-line style
// and line endings. Entries that are already declared are skipped. A name
// that exists only as a comment is refused so the operator uncomments it.
func (f *File) Add(cfg Config, add []NewEntry) ([]byte, error) {
	if !prefixRE.MatchString(cfg.PathPrefix) || strings.HasSuffix(cfg.PathPrefix, "/") || !mountRE.MatchString(cfg.Mount) {
		return nil, errors.New("customer_secrets.yml: invalid Vault path prefix or mount")
	}
	var block []string
	adding := map[string]bool{}
	for _, n := range add {
		if !secretNameRE.MatchString(n.VarName) {
			return nil, &EditError{n.VarName, "not a valid secret name (" + SecretNamePattern + ")"}
		}
		field := n.Field
		if field == "" {
			field = n.VarName
		}
		if !fieldRE.MatchString(field) {
			return nil, &EditError{n.VarName, "Vault field must match " + fieldRE.String()}
		}
		if _, ok := f.Lookup(n.VarName); ok || adding[n.VarName] {
			continue
		}
		if _, ok := f.commented[EnvPrefix+n.VarName]; ok {
			return nil, &EditError{n.VarName, "is commented out; uncomment it instead of adding a second entry"}
		}
		adding[n.VarName] = true
		if f.blankBetween && (len(f.entries) > 0 || len(block) > 0) {
			block = append(block, "")
		}
		pad, child := strings.Repeat(" ", f.entryIndent), strings.Repeat(" ", f.childIndent)
		block = append(block,
			pad+EnvPrefix+n.VarName+":",
			child+"vault: "+cfg.PathPrefix+"/"+field+"@"+cfg.Mount,
			child+"file: false")
	}
	if len(block) == 0 {
		return append([]byte(nil), f.src...), nil
	}
	if f.emptyMapping {
		// "secrets:" may carry an explicit null or {}; only bare "secrets:"
		// is extended.
		if strings.TrimSpace(strings.SplitN(f.lines[f.secretsLine-1], ":", 2)[1]) != "" {
			return nil, errors.New("customer_secrets.yml: secrets: has an inline value; make it an empty block first")
		}
	}
	lines := append([]string(nil), f.lines[:f.insertAfter]...)
	lines = append(lines, block...)
	lines = append(lines, f.lines[f.insertAfter:]...)
	out := []byte(strings.Join(lines, f.newline))
	// Verify: existing entries unchanged, new entries present.
	g, err := Parse(out)
	if err != nil {
		return nil, fmt.Errorf("customer_secrets.yml: edited file does not parse: %w", err)
	}
	for i, e := range f.entries {
		ge := g.entries[i]
		if ge.EnvVar != e.EnvVar || ge.VaultPath != e.VaultPath || ge.Field != e.Field || ge.Mount != e.Mount {
			return nil, errors.New("customer_secrets.yml: edit would change an existing entry")
		}
	}
	for name := range adding {
		if _, ok := g.Lookup(name); !ok {
			return nil, errors.New("customer_secrets.yml: edit did not add " + name)
		}
	}
	return out, nil
}
