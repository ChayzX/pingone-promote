// Package deploy keeps the deployment artefacts honest: the example
// configuration must decode into the server's Config, the systemd unit must
// keep its hardening, and the Kubernetes manifest must stay non-root.
package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"pingone-promote/internal/server"
)

func decodeConfig(t *testing.T, raw []byte) server.Config {
	t.Helper()
	var c server.Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("config does not decode into server.Config: %v", err)
	}
	return c
}

func checkExample(t *testing.T, c server.Config) {
	t.Helper()
	if c.SecretsFile == "" || c.GitLab.BaseURL == "" || c.PingOne.RegionCode == "" {
		t.Error("required fields missing")
	}
	if len(c.Sources) != 1 || c.Sources[0] != "development" || len(c.Rollout) != 3 {
		t.Errorf("unexpected sources/rollout: %v %v", c.Sources, c.Rollout)
	}
	if c.BranchPrefix != "CIAM-" || c.ApplyJobPattern != "apply:{env}" {
		t.Error("branchPrefix/applyJobPattern differ from the documented example")
	}
	if c.Tools.TerraformVersion != "1.16.7" || c.Tools.TerraformerVersion != "0.6.0" {
		t.Error("tool versions differ from scripts/fetch-tools.sh")
	}
	if !strings.HasPrefix(c.Tools.TerraformSHA256, "REPLACE_WITH_") {
		t.Error("terraformSha256 must stay a placeholder")
	}
	if len(c.Tools.TerraformerSHA256) != 64 {
		t.Error("terraformerSha256 must be a sha256 digest")
	}
}

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("\r")) {
		t.Errorf("%s has CRLF line endings", p)
	}
	return b
}

func TestExampleConfigDecodes(t *testing.T) {
	c := decodeConfig(t, read(t, "config.example.json"))
	checkExample(t, c)
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("listen = %q", c.Listen)
	}
}

func TestSecretsExampleNamesOnly(t *testing.T) {
	for _, line := range strings.Split(string(read(t, "secrets.example.env")), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || v != "" || !strings.HasPrefix(k, "PINGONE_PROMOTE_") {
			t.Errorf("secrets example line %q must be NAME= with an empty value", line)
		}
	}
}

func TestSystemdUnitHardening(t *testing.T) {
	unit := string(read(t, "systemd/pingone-promote.service"))
	for _, want := range []string{
		"User=pingone-promote", "DynamicUser=no", "Restart=on-failure",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "PrivateTmp=yes",
		"PrivateDevices=yes", "ProtectKernelTunables=yes", "ProtectKernelModules=yes",
		"ProtectKernelLogs=yes", "ProtectControlGroups=yes",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "RestrictNamespaces=yes",
		"LockPersonality=yes", "MemoryDenyWriteExecute=yes", "SystemCallFilter=@system-service",
		"\nCapabilityBoundingSet=\n", "UMask=0077", "ReadOnlyPaths=",
		"ExecStart=/opt/pingone-promote/pingone-promote serve --config ",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q", strings.TrimSpace(want))
		}
	}
}

type container struct {
	Name            string `yaml:"name"`
	SecurityContext struct {
		RunAsNonRoot             bool  `yaml:"runAsNonRoot"`
		ReadOnlyRootFilesystem   bool  `yaml:"readOnlyRootFilesystem"`
		AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
		Capabilities             struct {
			Drop []string `yaml:"drop"`
		} `yaml:"capabilities"`
	} `yaml:"securityContext"`
}

type deployment struct {
	Spec struct {
		Replicas int `yaml:"replicas"`
		Template struct {
			Spec struct {
				SecurityContext struct {
					RunAsNonRoot bool `yaml:"runAsNonRoot"`
					RunAsUser    int  `yaml:"runAsUser"`
					RunAsGroup   int  `yaml:"runAsGroup"`
					FSGroup      int  `yaml:"fsGroup"`
					Seccomp      struct {
						Type string `yaml:"type"`
					} `yaml:"seccompProfile"`
				} `yaml:"securityContext"`
				Containers     []container `yaml:"containers"`
				InitContainers []container `yaml:"initContainers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func TestKubernetesManifests(t *testing.T) {
	dec := yaml.NewDecoder(bytes.NewReader(read(t, "kubernetes/deployment.yaml")))
	kinds := map[string]map[string]any{}
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc != nil {
			kinds[doc["kind"].(string)] = doc
		}
	}
	for _, k := range []string{"ConfigMap", "Deployment", "Service"} {
		if kinds[k] == nil {
			t.Fatalf("missing %s", k)
		}
	}
	cm := kinds["ConfigMap"]["data"].(map[string]any)["config.json"].(string)
	checkExample(t, decodeConfig(t, []byte(cm)))

	var dep deployment
	b, _ := yaml.Marshal(kinds["Deployment"])
	if err := yaml.Unmarshal(b, &dep); err != nil {
		t.Fatal(err)
	}
	ps := dep.Spec.Template.Spec
	if dep.Spec.Replicas != 1 {
		t.Error("replicas must be 1")
	}
	sc := ps.SecurityContext
	if !sc.RunAsNonRoot || sc.RunAsUser != 10001 || sc.RunAsGroup != 10001 || sc.FSGroup != 10001 || sc.Seccomp.Type != "RuntimeDefault" {
		t.Errorf("pod securityContext is weaker than required: %+v", sc)
	}
	if len(ps.Containers) == 0 {
		t.Fatal("no containers")
	}
	all := append(append([]container{}, ps.Containers...), ps.InitContainers...)
	for _, c := range all {
		if !c.SecurityContext.RunAsNonRoot {
			t.Errorf("%s: runAsNonRoot missing", c.Name)
		}
		if !c.SecurityContext.ReadOnlyRootFilesystem {
			t.Errorf("%s: readOnlyRootFilesystem missing", c.Name)
		}
		if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
			t.Errorf("%s: allowPrivilegeEscalation must be false", c.Name)
		}
		if len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
			t.Errorf("%s: must drop ALL capabilities", c.Name)
		}
	}
}

func TestNetworkPolicyParses(t *testing.T) {
	var np map[string]any
	if err := yaml.Unmarshal(read(t, "kubernetes/networkpolicy.yaml"), &np); err != nil || np["kind"] != "NetworkPolicy" {
		t.Fatalf("networkpolicy.yaml: %v", err)
	}
}

func TestContainerfile(t *testing.T) {
	cf := string(read(t, "container/Containerfile"))
	for _, want := range []string{
		"fetch-tools.sh --dest /opt/pingone-promote/tools", "CGO_ENABLED=0", "-trimpath",
		"USER 10001:10001", "EXPOSE 8080", "ubi9/ubi-minimal",
		`"serve", "--config", "/etc/pingone-promote/config.json"`,
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("Containerfile lacks %q", want)
		}
	}
}

// loadAsServer runs the server's own configuration check on an example,
// with the placeholder digest replaced.
func loadAsServer(t *testing.T, raw []byte) server.Config {
	t.Helper()
	fixed := strings.Replace(string(raw), "REPLACE_WITH_sha256sum_OF_tools/terraform", strings.Repeat("a", 64), 1)
	p := t.TempDir() + "/config.json"
	if err := os.WriteFile(p, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := server.LoadConfig(p)
	if err != nil {
		t.Fatalf("the server refuses the example: %v", err)
	}
	return c
}

func TestExamplesPassTheServerCheck(t *testing.T) {
	c := loadAsServer(t, read(t, "config.example.json"))
	if c.Exposure != "" || !server.LoopbackListen(c.Listen) {
		t.Errorf("the plain example must stay on loopback: %q %q", c.Listen, c.Exposure)
	}
	dec := yaml.NewDecoder(bytes.NewReader(read(t, "kubernetes/deployment.yaml")))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc["kind"] != "ConfigMap" {
			continue
		}
		k := loadAsServer(t, []byte(doc["data"].(map[string]any)["config.json"].(string)))
		// The pod binds every interface behind the cluster proxy: that needs
		// the explicit opt-in and the proxy's host name.
		if k.Exposure != server.ExposureBehindProxy || len(k.AllowedHosts) == 0 {
			t.Errorf("kubernetes config: exposure %q, allowedHosts %v", k.Exposure, k.AllowedHosts)
		}
		// Never the Secret volume itself (its files are symlinks the server
		// refuses): the init container's owner-only copy.
		if k.SecretsFile != "/run/pingone-promote/secrets.env" {
			t.Errorf("secretsFile %q must be the init container's copy", k.SecretsFile)
		}
	}
}
