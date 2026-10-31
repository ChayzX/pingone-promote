package gitlab

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type Config struct {
	BaseURL     string `json:"baseUrl"`
	ProjectID   string `json:"projectId"`
	TokenEnv    string `json:"tokenEnv"`
	Environment string `json:"environment"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var numericID = regexp.MustCompile(`^[0-9]+$`)
var safeEnvironment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("GitLab base URL is required")
	}
	u, err := parseBase(c.BaseURL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("GitLab base URL must be an HTTPS origin and path without credentials, query, or fragment")
	}
	decodedPath, e := urlPathUnescape(u.EscapedPath())
	if e != nil {
		return errors.New("GitLab base URL cannot contain traversal")
	}
	for _, part := range strings.Split(decodedPath, "/") {
		if part == "." || part == ".." {
			return errors.New("GitLab base URL cannot contain traversal")
		}
	}
	if !numericID.MatchString(c.ProjectID) {
		return errors.New("GitLab project ID must be numeric")
	}
	if c.TokenEnv != "" && !envName.MatchString(c.TokenEnv) {
		return errors.New("GitLab token environment variable name is invalid")
	}
	if c.Environment != "" && !safeEnvironment.MatchString(c.Environment) {
		return errors.New("GitLab environment name is invalid")
	}
	return nil
}

func (c Config) withDefaults() Config {
	if c.TokenEnv == "" {
		c.TokenEnv = "GITLAB_TOKEN"
	}
	if c.Environment == "" {
		c.Environment = "production"
	}
	return c
}

// LoadConfig reads metadata only. An absent settings file is an unset configuration.
func LoadConfig(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "gitlab.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, errors.New("GitLab configuration is malformed")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("GitLab configuration is malformed")
	}
	if err = c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SaveConfig atomically stores only GitLab connection metadata, never a token value.
func SaveConfig(dir string, c Config) error {
	c = c.withDefaults()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, ".gitlab-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, "gitlab.json")); err != nil {
		return err
	}
	return nil
}

// validName avoids path separators and control characters in user supplied request IDs.
func validName(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
