package exporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type providerCall struct {
	Argv, Env          []string
	Dir, Bin           string
	CLI, Lock, Archive string
	Modes              map[string]uint32
}

func providerFixture(t *testing.T) (*ProviderStateReader, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "terraform")
	cmd := exec.Command("go", "build", "-o", bin, "-ldflags", "-X main.fixture="+dir, "./exportertest/providerreader")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	zip := filepath.Join(dir, "provider.zip")
	if err := os.WriteFile(zip, []byte("verified provider archive fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return &ProviderStateReader{Config: ProviderConfig{Terraform: bin, TerraformSHA256: providerFileHash(t, bin), ProviderZip: zip, ProviderSHA256: providerFileHash(t, zip), ProviderVersion: "1.24.0"}, Timeout: 5 * time.Second}, dir
}
func providerFileHash(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func providerCalls(t *testing.T, dir string) []providerCall {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	var calls []providerCall
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var call providerCall
		if e := json.Unmarshal([]byte(line), &call); e != nil {
			t.Fatal(e)
		}
		calls = append(calls, call)
	}
	return calls
}
func providerResources() []SourceResource {
	return []SourceResource{{Resource: Resource{Address: Address{Type: "pingone_population", Label: "primary"}}, ImportID: devEnv + "/22222222-2222-4222-8222-222222222222"}}
}

// Catches changed argv, inherited secrets/hooks, wrong environment scope, and persistent state.
func TestProviderReaderIsolatedImportProtocol(t *testing.T) {
	r, dir := providerFixture(t)
	t.Setenv("TF_LOG", "TRACE")
	t.Setenv("TF_VAR_password", "INHERITED_SECRET")
	t.Setenv("GITLAB_TOKEN", "INHERITED_SECRET")
	t.Setenv("PINGONE_API_ACCESS_TOKEN", "INHERITED_SECRET")
	c := creds
	c.ExportEnvironmentID = "33333333-3333-4333-8333-333333333333"
	resources := providerResources()
	resources[0].ImportID = c.ExportEnvironmentID + "/22222222-2222-4222-8222-222222222222"
	resources = append(resources, SourceResource{Resource: Resource{Address: Address{Type: "pingone_population", Label: "secondary"}}, ImportID: c.ExportEnvironmentID + "/44444444-4444-4444-8444-444444444444/55555555-5555-4555-8555-555555555555"})
	result, err := r.Read(context.Background(), c, resources)
	if err != nil {
		t.Fatal(err)
	}
	if result.EnvironmentID != c.ExportEnvironmentID {
		t.Fatalf("wrong exported environment: %q", result.EnvironmentID)
	}
	calls := providerCalls(t, dir)
	want := [][]string{{"--version"}, {"init", "-backend=false", "-input=false"}, {"providers", "schema", "-json"}, {"import", "-input=false", "-no-color", "pingone_population.primary", resources[0].ImportID}, {"import", "-input=false", "-no-color", "pingone_population.secondary", resources[1].ImportID}, {"show", "-json"}}
	if len(calls) != len(want) {
		t.Fatalf("calls: %#v", calls)
	}
	allowed := map[string]bool{"HOME": true, "PATH": true, "TF_IN_AUTOMATION": true, "TF_INPUT": true, "CHECKPOINT_DISABLE": true, "TF_CLI_CONFIG_FILE": true, "PINGONE_CLIENT_ID": true, "PINGONE_CLIENT_SECRET": true, "PINGONE_ENVIRONMENT_ID": true, "PINGONE_REGION_CODE": true}
	for i, call := range calls {
		if !reflect.DeepEqual(call.Argv, want[i]) {
			t.Fatalf("command %d: %v", i, call.Argv)
		}
		if call.Archive != "verified provider archive fixture" {
			t.Fatal("verified archive was not staged under standard mirror filename")
		}
		if strings.Contains(call.CLI, "direct") || !strings.Contains(call.CLI, "filesystem_mirror") || !strings.Contains(call.CLI, "registry.terraform.io/pingidentity/pingone") {
			t.Fatal("unsafe provider installation configuration")
		}
		if !strings.Contains(call.Lock, "zh:"+r.Config.ProviderSHA256) || !strings.Contains(call.Lock, `version = "1.24.0"`) {
			t.Fatal("provider lock lacks pinned archive hash")
		}
		for name, mode := range call.Modes {
			if mode != 0600 && mode != 0700 {
				t.Fatalf("unsafe mode %o for %s", mode, name)
			}
		}
		if call.Bin == r.Config.Terraform {
			t.Fatal("executed original binary")
		}
		for _, env := range call.Env {
			k, v, _ := strings.Cut(env, "=")
			if !allowed[k] {
				t.Fatalf("unexpected env: %s", k)
			}
			if k == "PINGONE_ENVIRONMENT_ID" && v != creds.WorkerEnvironmentID {
				t.Fatal("worker environment replaced by source")
			}
			if strings.Contains(v, "INHERITED_SECRET") {
				t.Fatal("inherited secret")
			}
			if strings.HasPrefix(k, "PINGONE_") && call.Argv[0] != "import" {
				t.Fatalf("credentials in command %d", i)
			}
		}
		if _, err := os.Stat(call.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("workspace persists: %v", err)
		}
	}
	for _, data := range result.Files {
		if strings.Contains(string(data), "COMPUTED_SECRET") || strings.Contains(string(data), creds.ClientSecret) {
			t.Fatal("secret leaked")
		}
	}
}

func TestProviderReaderRejectsPinsBeforeExecution(t *testing.T) {
	for _, pin := range []string{"terraform", "provider", "missing", "version", "tampered-binary", "tampered-archive"} {
		t.Run(pin, func(t *testing.T) {
			r, dir := providerFixture(t)
			switch pin {
			case "terraform":
				r.Config.TerraformSHA256 = strings.Repeat("0", 64)
			case "provider":
				r.Config.ProviderSHA256 = strings.Repeat("0", 64)
			case "missing":
				r.Config.TerraformSHA256 = ""
			case "version":
				r.Config.ProviderVersion = "1.23.0"
			case "tampered-binary":
				file, _ := os.OpenFile(r.Config.Terraform, os.O_APPEND|os.O_WRONLY, 0)
				file.Write([]byte("tampered"))
				file.Close()
			case "tampered-archive":
				os.WriteFile(r.Config.ProviderZip, []byte("tampered"), 0600)
			}
			if _, err := r.Read(context.Background(), creds, providerResources()); err == nil {
				t.Fatal("untrusted pin accepted")
			}
			if calls := providerCalls(t, dir); len(calls) != 0 {
				t.Fatalf("executed before pins checked: %v", calls)
			}
		})
	}
}
func TestProviderReaderStopsAndCleansUp(t *testing.T) {
	for _, mode := range []string{"failure", "overflow", "version", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			r, dir := providerFixture(t)
			os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600)
			if mode == "timeout" {
				r.Timeout = 100 * time.Millisecond
			}
			if mode == "overflow" {
				r.MaxOutput = 256
			}
			_, err := r.Read(context.Background(), creds, providerResources())
			if err == nil {
				t.Fatal("expected failure")
			}
			var pe *ProviderReadError
			if !errors.As(err, &pe) {
				t.Fatalf("untyped error: %T %v", err, err)
			}
			if strings.Contains(err.Error(), "CLIENT_SECRET") || strings.Contains(err.Error(), creds.ClientSecret) {
				t.Fatal("diagnostic leaked")
			}
			for _, call := range providerCalls(t, dir) {
				if _, err := os.Stat(call.Dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("workspace persists")
				}
				if call.Argv[0] == "show" && mode == "failure" {
					t.Fatal("continued after failed import")
				}
			}
		})
	}
}
func TestProviderReaderRejectsUnsafeResources(t *testing.T) {
	for _, kind := range []string{"user", "address", "id", "scope", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			r, dir := providerFixture(t)
			resources := providerResources()
			switch kind {
			case "user":
				resources[0].Type = "pingone_user"
			case "address":
				resources[0].Label = "a[0]"
			case "id":
				resources[0].ImportID = devEnv + "/../bad"
			case "scope":
				resources[0].ImportID = "33333333-3333-4333-8333-333333333333/id"
			case "duplicate":
				resources = append(resources, resources[0])
			}
			if _, err := r.Read(context.Background(), creds, resources); err == nil {
				t.Fatal("unsafe resource accepted")
			}
			if len(providerCalls(t, dir)) != 0 {
				t.Fatal("executed unsafe input")
			}
		})
	}
}

// This opt-in fixture executes the actual pinned provider through the production
// reader against a local TLS API. The proxy redirects every CONNECT locally:
// neither real authentication nor a real PingOne tenant is contacted.
func TestProviderReaderRealProviderTLSAPI(t *testing.T) {
	archive := os.Getenv("PINGONE_PROMOTE_PROVIDER_ZIP")
	terraform := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if archive == "" || terraform == "" {
		t.Skip("set verified provider ZIP and Terraform paths for real provider fixture")
	}
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local PingOne API fixture"}, DNSNames: []string{"api.pingone.com", "auth.pingone.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	var managementWrites []string
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		requests = append(requests, req.Method+" "+req.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/as/token") {
			io.WriteString(w, `{"access_token":"fixture-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if req.Method != "GET" {
			mu.Lock()
			managementWrites = append(managementWrites, req.Method+" "+req.URL.Path)
			mu.Unlock()
			http.Error(w, "management writes forbidden", 405)
			return
		}
		if strings.Contains(req.URL.Path, "/populations/") {
			io.WriteString(w, `{"id":"22222222-2222-4222-8222-222222222222","name":"Fixture population","environment":{"id":"11111111-1111-4111-8111-111111111111"},"default":false}`)
			return
		}
		if strings.HasSuffix(req.URL.Path, "/environments/"+devEnv) {
			io.WriteString(w, `{"id":"11111111-1111-4111-8111-111111111111","type":"SANDBOX","name":"Fixture"}`)
			return
		}
		http.Error(w, "unexpected fixture endpoint", 404)
	}))
	api.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	api.StartTLS()
	defer api.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "CONNECT" {
			http.Error(w, "CONNECT required", 405)
			return
		}
		upstream, err := net.Dial("tcp", api.Listener.Addr().String())
		if err != nil {
			http.Error(w, "fixture connection failed", 502)
			return
		}
		downstream, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		downstream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { defer downstream.Close(); defer upstream.Close(); io.Copy(upstream, downstream) }()
		io.Copy(downstream, upstream)
	}))
	defer proxy.Close()
	r := &ProviderStateReader{Config: ProviderConfig{Terraform: terraform, TerraformSHA256: providerFileHash(t, terraform), ProviderZip: archive, ProviderSHA256: providerFileHash(t, archive), ProviderVersion: "1.24.0"}, Network: Network{HTTPSProxy: proxy.URL, CAFile: ca}, Timeout: 180 * time.Second}
	fixtureCreds := creds
	fixtureCreds.ClientID = "44444444-4444-4444-8444-444444444444"
	result, err := r.Read(context.Background(), fixtureCreds, providerResources())
	mu.Lock()
	observed := append([]string(nil), requests...)
	writes := append([]string(nil), managementWrites...)
	mu.Unlock()
	if err != nil {
		t.Fatalf("real reader failed: %v; fixture requests: %v", err, observed)
	}
	if len(writes) > 0 {
		t.Fatalf("provider attempted management writes: %v", writes)
	}
	if !strings.Contains(strings.Join(observed, "\n"), "GET /v1/environments/"+devEnv+"/populations/22222222-2222-4222-8222-222222222222") {
		t.Fatalf("population was not read: %v", observed)
	}
	content := string(result.Files[ModuleDir+"/pingone_population.tf"])
	if !strings.Contains(content, "Fixture population") {
		t.Fatalf("provider state was not rendered: %s", content)
	}
	t.Logf("actual Terraform 1.16.7 / provider 1.24.0 populated required name from environment-only import configuration; %d local API/auth requests; no management writes", len(observed))
}

func TestProviderReaderAcceptsRecipeImportIDs(t *testing.T) {
	for _, id := range []string{devEnv, devEnv + "/en-US", devEnv + "/template-name/22222222-2222-4222-8222-222222222222", devEnv + "/22222222-2222-4222-8222-222222222222/44444444-4444-4444-8444-444444444444"} {
		if !providerImportID(id, devEnv) {
			t.Fatalf("valid recipe import ID was rejected: %s", id)
		}
	}
}

func TestProviderReaderKillsDescendantsOnCancellationAndParentExit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process groups require supported Unix platform")
	}
	for _, mode := range []string{"descendant", "descendant-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, dir := providerFixture(t)
			r.Timeout = 30 * time.Second
			os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600)
			t.Cleanup(func() {
				b, _ := os.ReadFile(filepath.Join(dir, "child-pid"))
				pid, _ := strconv.Atoi(string(b))
				if pid > 0 {
					p, _ := os.FindProcess(pid)
					if p != nil {
						p.Kill()
					}
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := r.Read(ctx, creds, providerResources()); result <- err }()
			if mode == "descendant" {
				deadline := time.Now().Add(20 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "child-address")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						<-result
						t.Fatal("child was never ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("expected cancellation or parent-exit failure")
				}
			case <-time.After(35 * time.Second):
				cancel()
				t.Fatal("reader did not terminate")
			}

			address, err := os.ReadFile(filepath.Join(dir, "child-address"))
			if err != nil {
				t.Fatalf("child was never started: %v", err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				connection, err := net.DialTimeout("tcp", string(address), 50*time.Millisecond)
				if err != nil {
					break
				}
				connection.Close()
				if time.Now().After(deadline) {
					t.Fatal("provider descendant still accepts connections after reader returned")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestProviderReaderPrivateTreeRejectsSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if providerPrivateTree(root) {
		t.Fatal("unsupported symlink accepted")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions outside workspace changed")
	}
}
