package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab"
)

// The fixture values are distinctive so a test can prove that no error
// ever carries them.
const (
	fixtureID     = "fixture-id-7c1e"
	fixtureSecret = "fixture-value-9d2a=with=equals"
	fixtureToken  = "fixture-token-4b8f"
)

var fixtureSecrets = "# worker and GitLab credentials\n\n" +
	SecretWorkerClientID + "=" + fixtureID + "\n" +
	SecretWorkerClientSecret + "=" + fixtureSecret + "\n" +
	SecretGitLabToken + "=\"" + fixtureToken + "\"\n"

var fixtureValues = []string{fixtureID, fixtureSecret, fixtureToken, "fixture-value-9d2a", "another-fixture-value", "unexpected-fixture-name"}

func noValueIn(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, v := range fixtureValues {
		if strings.Contains(err.Error(), v) {
			t.Fatalf("error %q contains a value from the file", err)
		}
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes and owners are POSIX; Windows is not a supported host")
	}
}

func writeSecrets(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecretsFileModes(t *testing.T) {
	skipOnWindows(t)
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, true}, {0o400, true},
		{0o640, false}, {0o644, false}, {0o440, false}, {0o604, false}, {0o660, false}, {0o700, false}, {0o602, false}, {0o620, false},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			s, err := LoadSecrets(writeSecrets(t, fixtureSecrets, tc.mode))
			if (err == nil) != tc.ok {
				t.Fatalf("mode %04o accepted=%v, want %v (%v)", tc.mode, err == nil, tc.ok, err)
			}
			noValueIn(t, err)
			if tc.ok && (s.WorkerClientID != fixtureID || s.WorkerClientSecret != fixtureSecret || s.GitLabToken != fixtureToken) {
				t.Fatal("values were not loaded intact")
			}
		})
	}
}

func TestSecretsFileRefusesSymlink(t *testing.T) {
	skipOnWindows(t)
	target := writeSecrets(t, fixtureSecrets, 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSecrets(link)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink accepted or unclear error: %v", err)
	}
	noValueIn(t, err)
}

func TestSecretsFileRefusesDirectoryAndMissing(t *testing.T) {
	if _, err := LoadSecrets(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := LoadSecrets(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestSecretsFileOwnerIsChecked(t *testing.T) {
	skipOnWindows(t)
	if os.Getuid() == 0 {
		// root can chown to another user and check the refusal.
		p := writeSecrets(t, fixtureSecrets, 0o600)
		if err := os.Chown(p, 65534, -1); err != nil {
			t.Skip("cannot chown:", err)
		}
		_, err := LoadSecrets(p)
		if err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Fatalf("foreign owner accepted: %v", err)
		}
		noValueIn(t, err)
		return
	}
	// Unprivileged: the file the test wrote is owned by the test's user.
	if _, err := LoadSecrets(writeSecrets(t, fixtureSecrets, 0o600)); err != nil {
		t.Fatalf("own file refused: %v", err)
	}
}

func TestSecretsWindowsRefusesUnverifiable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	_, err := LoadSecrets(writeSecrets(t, fixtureSecrets, 0o600))
	if err == nil {
		t.Fatal("Windows cannot verify owner-only access; the file must be refused")
	}
	noValueIn(t, err)
}

func TestParseSecrets(t *testing.T) {
	s, err := parseSecrets(strings.NewReader(fixtureSecrets))
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkerClientSecret != fixtureSecret {
		t.Fatal("a value containing '=' was not kept intact")
	}
	if s.GitLabToken != fixtureToken || s.WorkerClientID != fixtureID {
		t.Fatal("values were not loaded")
	}
	for name, content := range map[string]string{
		"duplicate":    fixtureSecrets + SecretGitLabToken + "=another-fixture-value\n",
		"unknown":      fixtureSecrets + "unexpected-fixture-name=another-fixture-value\n",
		"no separator": fixtureSecrets + "another-fixture-value\n",
		"empty name":   fixtureSecrets + "=another-fixture-value\n",
		"empty value":  strings.Replace(fixtureSecrets, fixtureToken, "", 1),
		"missing name": SecretWorkerClientID + "=" + fixtureID + "\n" + SecretWorkerClientSecret + "=" + fixtureSecret + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseSecrets(strings.NewReader(content))
			if err == nil {
				t.Fatal("invalid file accepted")
			}
			noValueIn(t, err)
		})
	}
}

// validConfig is a minimal accepted configuration.
func validConfig() map[string]any {
	digest := strings.Repeat("a", 64)
	return map[string]any{
		"secretsFile": "/etc/pingone-promote/secrets",
		"gitlab":      map[string]any{"baseUrl": "https://gitlab.example.com", "projectId": "42"},
		"pingone":     map[string]any{"regionCode": "NA", "workerEnvironmentId": "11111111-1111-4111-8111-111111111111"},
		"sources":     []string{"development"},
		"rollout":     []string{"testing", "production"},
		"tools": map[string]any{"terraform": "/usr/local/bin/terraform", "terraformSha256": digest,
			"terraformer": "/usr/local/bin/terraformer", "terraformerVersion": "0.6.0", "terraformerSha256": digest},
	}
}

func loadRaw(t *testing.T, raw string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(p)
}

func loadMap(t *testing.T, m map[string]any) (Config, error) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return loadRaw(t, string(b))
}

func TestConfigDefaults(t *testing.T) {
	c, err := loadMap(t, validConfig())
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Fatalf("listen default %q", c.Listen)
	}
	if c.SignIn != "none" {
		t.Fatalf("signIn default %q", c.SignIn)
	}
	if c.BranchPrefix != gitlab.RequestBranchPrefix {
		t.Fatalf("branchPrefix default %q", c.BranchPrefix)
	}
	if c.ApplyJobPattern != "apply:{env}" {
		t.Fatalf("applyJobPattern default %q", c.ApplyJobPattern)
	}
	if c.Tools.TerraformVersion != "1.16.7" {
		t.Fatalf("terraformVersion default %q", c.Tools.TerraformVersion)
	}
}

func TestConfigAcceptsExplicitValues(t *testing.T) {
	m := validConfig()
	m["listen"] = "0.0.0.0:8443"
	m["tlsCertFile"], m["tlsKeyFile"] = "/etc/tls/cert.pem", "/etc/tls/key.pem"
	m["exposure"] = "tls"
	m["allowedHosts"] = []string{"promote.example.com"}
	m["signIn"] = "none"
	m["branchPrefix"] = "CIAM-"
	m["applyJobPattern"] = "deploy {env}"
	c, err := loadMap(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchPrefix != "CIAM-" || c.ApplyJobPattern != "deploy {env}" || c.Listen != "0.0.0.0:8443" {
		t.Fatalf("%+v", c)
	}
}

func TestConfigExposure(t *testing.T) {
	cases := []struct {
		listen, exposure string
		tls, hosts       bool
		ok               bool
	}{
		{"127.0.0.1:8080", "", false, false, true},
		{"localhost:8080", "", false, false, true},
		{"[::1]:8080", "", false, false, true},
		{"0.0.0.0:8080", "", false, false, false},
		{":8080", "", false, false, false},
		{"10.1.2.3:8080", "", true, true, false},
		{"0.0.0.0:8443", "tls", true, false, true},
		{"0.0.0.0:8443", "tls", false, true, false},
		{"0.0.0.0:8080", "behind-proxy", false, true, true},
		{"0.0.0.0:8080", "behind-proxy", false, false, false},
		{"0.0.0.0:8080", "public", true, true, false},
		{"0.0.0.0:8080", "container", false, false, true},
		{"0.0.0.0:8080", "container", false, true, false},
		{"0.0.0.0:8080", "container", true, false, false},
	}
	for _, tc := range cases {
		m := validConfig()
		m["listen"] = tc.listen
		if tc.exposure != "" {
			m["exposure"] = tc.exposure
		}
		if tc.tls {
			m["tlsCertFile"], m["tlsKeyFile"] = "/etc/tls/cert.pem", "/etc/tls/key.pem"
		}
		if tc.hosts {
			m["allowedHosts"] = []string{"promote.example.com"}
		}
		if _, err := loadMap(t, m); (err == nil) != tc.ok {
			t.Errorf("listen %s exposure %q tls=%v hosts=%v: err=%v, want ok=%v", tc.listen, tc.exposure, tc.tls, tc.hosts, err, tc.ok)
		}
	}
}

func TestConfigRefusals(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"signIn pingone":        func(m map[string]any) { m["signIn"] = "pingone" },
		"signIn other":          func(m map[string]any) { m["signIn"] = "oidc" },
		"unknown top field":     func(m map[string]any) { m["workerClientSecret"] = "fixture-private" },
		"unknown nested field":  func(m map[string]any) { m["gitlab"].(map[string]any)["token"] = "fixture-private" },
		"duplicate source":      func(m map[string]any) { m["sources"] = []string{"development", "development"} },
		"duplicate rollout":     func(m map[string]any) { m["rollout"] = []string{"testing", "testing"} },
		"source in rollout":     func(m map[string]any) { m["rollout"] = []string{"development", "production"} },
		"bad env name":          func(m map[string]any) { m["rollout"] = []string{"../prod"} },
		"no sources":            func(m map[string]any) { m["sources"] = []string{} },
		"no rollout":            func(m map[string]any) { delete(m, "rollout") },
		"allowedHosts port":     func(m map[string]any) { m["allowedHosts"] = []string{"promote.example.com:8443"} },
		"allowedHosts scheme":   func(m map[string]any) { m["allowedHosts"] = []string{"https://promote.example.com"} },
		"allowedHosts wildcard": func(m map[string]any) { m["allowedHosts"] = []string{"*.example.com"} },
		"allowedHosts empty":    func(m map[string]any) { m["allowedHosts"] = []string{""} },
		"tls cert only":         func(m map[string]any) { m["tlsCertFile"] = "/etc/tls/cert.pem" },
		"tls key only":          func(m map[string]any) { m["tlsKeyFile"] = "/etc/tls/key.pem" },
		"listen no port":        func(m map[string]any) { m["listen"] = "127.0.0.1" },
		"no secretsFile":        func(m map[string]any) { delete(m, "secretsFile") },
		"gitlab http":           func(m map[string]any) { m["gitlab"].(map[string]any)["baseUrl"] = "http://gitlab.example.com" },
		"gitlab project":        func(m map[string]any) { m["gitlab"].(map[string]any)["projectId"] = "group/project" },
		"pingone region":        func(m map[string]any) { delete(m["pingone"].(map[string]any), "regionCode") },
		"branch prefix":         func(m map[string]any) { m["branchPrefix"] = "release" },
		"job pattern":           func(m map[string]any) { m["applyJobPattern"] = "apply" },
		"terraform version":     func(m map[string]any) { m["tools"].(map[string]any)["terraformVersion"] = "1.5.7" },
		"terraform digest":      func(m map[string]any) { m["tools"].(map[string]any)["terraformSha256"] = "abc" },
		"terraform digest uppercase": func(m map[string]any) {
			m["tools"].(map[string]any)["terraformSha256"] = strings.Repeat("A", 64)
		},
		"terraformer digest missing": func(m map[string]any) { delete(m["tools"].(map[string]any), "terraformerSha256") },
		"pingcli without digest":     func(m map[string]any) { m["tools"].(map[string]any)["pingcli"] = "/usr/local/bin/pingcli" },
		"pingcli bad digest": func(m map[string]any) {
			tools := m["tools"].(map[string]any)
			tools["pingcli"], tools["pingcliVersion"], tools["pingcliSha256"] = "/usr/local/bin/pingcli", "0.8.4", "not-a-digest"
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			m := validConfig()
			mut(m)
			_, err := loadMap(t, m)
			if err == nil {
				t.Fatal("invalid config accepted")
			}
			if strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("config error leaked a value")
			}
		})
	}
}

func TestConfigRejectTrailingDataAndMalformed(t *testing.T) {
	good, _ := json.Marshal(validConfig())
	for name, raw := range map[string]string{
		"trailing object":  string(good) + " {}",
		"trailing garbage": string(good) + " x",
		"two objects":      `{"secretsFile":"x"} {}`,
		"array":            `[]`,
		"truncated":        string(good[:len(good)-1]),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadRaw(t, raw); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("missing config accepted")
	}
}

// A configuration mistake names the setting or the line, never a value.
func TestConfigErrorsSayWhere(t *testing.T) {
	m := validConfig()
	m["branchPrefx"] = "fixture-private"
	_, err := loadMap(t, m)
	if err == nil || !strings.Contains(err.Error(), `"branchPrefx"`) || strings.Contains(err.Error(), "fixture-private") {
		t.Fatalf("unknown setting: %v", err)
	}
	m = validConfig()
	m["sources"] = "fixture-private"
	_, err = loadMap(t, m)
	if err == nil || !strings.Contains(err.Error(), `"sources"`) || strings.Contains(err.Error(), "fixture-private") {
		t.Fatalf("wrong kind: %v", err)
	}
	_, err = loadRaw(t, "{\n  \"listen\": \"fixture-private\",\n  \"signIn\": \"none\",\n}\n")
	if err == nil || !strings.Contains(err.Error(), "line 4") || strings.Contains(err.Error(), "fixture-private") {
		t.Fatalf("trailing comma: %v", err)
	}
	_, err = loadRaw(t, "{\n  \"listen\": \"x\"\n  \"signIn\": \"none\"\n}\n")
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("missing comma: %v", err)
	}
}

// TestConfigHasNoSecretField walks the JSON schema of Config: a secret must
// never be placeable in the configuration file. secretsFile is a path to
// the separate secrets file, not a value.
func TestConfigHasNoSecretField(t *testing.T) {
	// Paths to files, not values.
	allowed := map[string]bool{"secretsFile": true, "customerSecretsFile": true}
	var walk func(typ reflect.Type, prefix string)
	walk = func(typ reflect.Type, prefix string) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			lower := strings.ToLower(name)
			for _, bad := range []string{"secret", "token", "password", "passwd", "credential", "privatekey", "apikey"} {
				if strings.Contains(lower, bad) && !allowed[name] {
					t.Errorf("config field %s%s looks like it holds a secret", prefix, name)
				}
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, prefix+name+".")
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "")
	if f, ok := reflect.TypeOf(Config{}).FieldByName("SecretsFile"); !ok || f.Type.Kind() != reflect.String {
		t.Fatal("secretsFile must be a path string")
	}
}

// A systemd credential (LoadCredential=) is a 0440 copy inside the private
// $CREDENTIALS_DIRECTORY; it is accepted there and only there, and only
// while neither the file nor the directory gives anything to other users.
// (On a host the copy is root-owned with an ACL for the service user; the
// test, unprivileged, owns both.)
func TestSecretsSystemdCredential(t *testing.T) {
	skipOnWindows(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := credentialsRoot
	credentialsRoot = root + string(filepath.Separator)
	t.Cleanup(func() { credentialsRoot = old })
	credDir := filepath.Join(root, "pingone-promote.service")
	if err := os.Mkdir(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(credDir, "secrets")
	if err := os.WriteFile(p, []byte(fixtureSecrets), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(credDir, 0o700) })
	if _, err := LoadSecrets(p); err == nil {
		t.Fatal("a 0440 file must be refused when CREDENTIALS_DIRECTORY is not set")
	}
	// A directory outside the credentials root is not trusted.
	t.Setenv("CREDENTIALS_DIRECTORY", t.TempDir())
	if _, err := LoadSecrets(p); err == nil {
		t.Fatal("a credentials directory outside /run/credentials/ was trusted")
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credDir+"/../"+filepath.Base(credDir))
	if _, err := LoadSecrets(p); err == nil {
		t.Fatal("an unclean credentials directory path was trusted")
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credDir)
	if _, err := LoadSecrets(p); err != nil {
		t.Fatalf("systemd credential refused: %v", err)
	}
	// Readable by others: refused even inside the credentials directory.
	if err := os.Chmod(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecrets(p); err == nil {
		t.Fatal("a world-readable credential was accepted")
	} else {
		noValueIn(t, err)
	}
	// A file next to, not inside, the credentials directory gains nothing.
	other := writeSecrets(t, fixtureSecrets, 0o440)
	if _, err := LoadSecrets(other); err == nil {
		t.Fatal("a 0440 file outside the credentials directory was accepted")
	}
}

func TestSourceEnvironmentIDs(t *testing.T) {
	for name, tc := range map[string]struct {
		ids map[string]string
		ok  bool
	}{
		"valid":         {map[string]string{"development": "22222222-2222-4222-8222-222222222222"}, true},
		"not a source":  {map[string]string{"testing": "22222222-2222-4222-8222-222222222222"}, false},
		"unknown name":  {map[string]string{"qa": "22222222-2222-4222-8222-222222222222"}, false},
		"not an ID":     {map[string]string{"development": "dev-env"}, false},
		"empty mapping": {map[string]string{}, true},
	} {
		t.Run(name, func(t *testing.T) {
			m := validConfig()
			m["sourceEnvironmentIds"] = tc.ids
			c, err := loadMap(t, m)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && len(tc.ids) > 0 && c.SourceEnvironmentIDs["development"] == "" {
				t.Fatal("the ID was not kept")
			}
		})
	}
}

func TestBranchPattern(t *testing.T) {
	for name, tc := range map[string]struct {
		pattern, example string
		ok               bool
	}{
		"jira":             {"CIAM-[0-9]+", "CIAM-1234", true},
		"no example":       {"CIAM-[0-9]+", "", true},
		"bad regex":        {"CIAM-[0-9", "", false},
		"example mismatch": {"CIAM-[0-9]+", "ciam-12", false},
	} {
		t.Run(name, func(t *testing.T) {
			m := validConfig()
			m["branchPattern"], m["branchExample"] = tc.pattern, tc.example
			c, err := loadMap(t, m)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (!c.branchRE.MatchString("CIAM-77") || c.branchRE.MatchString("feature/CIAM-77") || c.branchRE.MatchString("CIAM-77x")) {
				t.Fatal("the pattern must match the whole name only")
			}
		})
	}
}
