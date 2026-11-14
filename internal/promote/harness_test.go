package promote

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
)

const (
	devEnv   = "11111111-1111-4111-8111-111111111111"
	qaEnv    = "22222222-2222-4222-8222-222222222222"
	stageEnv = "33333333-3333-4333-8333-333333333333"
	prodEnv  = "44444444-4444-4444-8444-444444444444"
)

var (
	fakeMu    sync.Mutex
	fakeCache = map[string]exportertest.Fake{}
)

func fakeFor(t *testing.T, fixture string) exportertest.Fake {
	t.Helper()
	fakeMu.Lock()
	defer fakeMu.Unlock()
	if f, ok := fakeCache[fixture]; ok {
		return f
	}
	dir, err := os.MkdirTemp("", "promote-fake-")
	if err != nil {
		t.Fatal(err)
	}
	f := exportertest.Build(t, dir, fixture)
	fakeCache[fixture] = f
	return f
}

var testCreds = exporter.Credentials{ClientID: "c", ClientSecret: "s", WorkerEnvironmentID: devEnv, RegionCode: "NA"}

var testSecretNames = map[string]string{"davinci_connection_PingOne_clientSecret": "PingOne_DaVinci_connection_PingOne_clientSecret"}

// neutralized exports seeds (or everything) from a fixture environment.
func neutralized(t *testing.T, fixture string, seeds ...string) *neutralize.Result {
	t.Helper()
	return neutralizedExcluding(t, fixture, nil, seeds...)
}

// neutralizedExcluding is neutralized with excluded resources. managed
// lists the excluded addresses that are already managed in Git.
func neutralizedExcluding(t *testing.T, fixture string, excluded []neutralize.Excluded, seeds ...string) *neutralize.Result {
	t.Helper()
	f := fakeFor(t, fixture)
	r := &exporter.Runner{Config: exporter.Config{Version: f.Version, SHA256: f.SHA256, Path: f.Path}}
	var patterns []string
	for _, e := range excluded {
		patterns = append(patterns, e.Address)
	}
	var exp *exporter.Export
	var err error
	if len(seeds) == 0 {
		exp, err = r.WholeExport(context.Background(), testCreds, patterns)
	} else {
		exp, err = r.ScopedExport(context.Background(), testCreds, seeds, patterns)
	}
	if err != nil {
		t.Fatal(err)
	}
	res, err := neutralize.Neutralize(exp.NeutralizeInput(),
		neutralize.Options{SourceEnvironmentID: devEnv, SecretNames: testSecretNames, Excluded: excluded})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readTree(t *testing.T, dir string) MapRepo {
	t.Helper()
	out := MapRepo{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeTree(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func apply(head MapRepo, res *MergeResult) MapRepo {
	out := MapRepo{}
	for k, v := range head {
		out[k] = v
	}
	for k, v := range res.Files {
		out[k] = v
	}
	return out
}

func envValues() map[string]map[string]cty.Value {
	vals := map[string]map[string]cty.Value{}
	for env, id := range map[string]string{"qa": qaEnv, "stage": stageEnv, "prod": prodEnv} {
		vals[env] = map[string]cty.Value{
			"pingone_environment_id":             cty.StringVal(id),
			"davinci_variable_companyName_value": cty.StringVal("Example Corp"),
			"davinci_application_Web_oauth_redirect_uris": cty.TupleVal([]cty.Value{
				cty.StringVal("https://auth.pingone.com/" + id + "/rp/callback/openid_connect")}),
		}
	}
	return vals
}

const fixtureCustomerSecrets = `# Example pipeline secret declarations (fixture).
.customer-secrets:
  secrets:
    TF_VAR_client_secret:
      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/client_secret@secret
      file: false
`

// TestRegenerateRepoFixture rebuilds testdata/repo, the "before" state of
// the config repository: a full export of the dev fixture, then edited
// back to an older Flow B (no subflow, older node title) and an older
// shared PingOne connection, plus hand comments the merge must keep.
func TestRegenerateRepoFixture(t *testing.T) {
	if os.Getenv("PINGONE_PROMOTE_REGEN_FIXTURE") == "" {
		t.Skip("set PINGONE_PROMOTE_REGEN_FIXTURE=1 to rebuild testdata/repo")
	}
	exp := neutralized(t, exportertest.FixtureDir("dev"))
	var all []string
	for _, b := range exp.Blocks {
		all = append(all, b.Address)
	}
	res, err := Merge(MergeRequest{Export: exp, Head: MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)},
		Selected: all, SourceEnv: "dev", Rollout: []string{"qa", "stage", "prod"}, EnvValues: envValues()})
	if err != nil {
		t.Fatal(err)
	}
	repo := apply(MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}, res)
	edit := func(p, key string, fn func([]byte) []byte) {
		f, err := hclblocks.Load(p, repo[p])
		if err != nil {
			t.Fatal(err)
		}
		i, _ := f.Find(key)
		if err := f.Replace(key, fn(f.Blocks[i].Text(f.Src))); err != nil {
			t.Fatal(err)
		}
		repo[p] = f.Src
	}
	drop := func(p, key string) {
		f, err := hclblocks.Load(p, repo[p])
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Remove(key); err != nil {
			t.Fatal(err)
		}
		repo[p] = f.Src
	}
	sub := "pingcli__Flow-0020-B-0020-Subflow"
	edit("definitions/pingone_davinci_flow.tf", "resource.pingone_davinci_flow.pingcli__Flow-0020-B", func(b []byte) []byte {
		s := strings.Replace(string(b), "Look up the user (v2)", "Look up the user", 1)
		start := strings.Index(s, "\n        \"n2bbbbbbbb\" = {")
		end := strings.Index(s[start:], "\n      }\n    }\n  }") + start
		return []byte(s[:start] + s[end:])
	})
	edit("definitions/pingone_davinci_connector_instance.tf", "resource.pingone_davinci_connector_instance.pingcli__PingOne", func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"value" : "EU"`), []byte(`"value" : "NA"`), 1)
	})
	for _, typ := range []string{"pingone_davinci_flow", "pingone_davinci_flow_deploy", "pingone_davinci_flow_enable"} {
		drop("definitions/"+typ+".tf", "resource."+typ+"."+sub)
	}
	// imports.tf without the subflow.
	f, _ := hclblocks.Load("imports.tf", repo["imports.tf"])
	var keep [][]byte
	for _, b := range f.Blocks {
		if !bytes.Contains(b.Text(f.Src), []byte(sub)) {
			keep = append(keep, b.Text(f.Src))
		}
	}
	repo["imports.tf"] = append(bytes.Join(keep, []byte("\n\n")), '\n')
	// The dev overlay must not adopt the subflow yet.
	ov, err := overlay.ParseOverlay(repo["dev.tfvars"], "dev.tfvars")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]cty.Value{}
	for it := ov.Values["import_ids"].ElementIterator(); it.Next(); {
		k, v := it.Element()
		if !strings.Contains(k.AsString(), sub) {
			ids[k.AsString()] = v
		}
	}
	if repo["dev.tfvars"], err = overlay.SetOverlay(repo["dev.tfvars"], "dev.tfvars", map[string]cty.Value{"import_ids": cty.ObjectVal(ids)}); err != nil {
		t.Fatal(err)
	}
	// Hand comments and a hand-maintained block the merge must keep.
	repo["definitions/pingone_davinci_flow.tf"] = append([]byte("# Flows are managed by pingone-promote; review in merge requests.\n"), repo["definitions/pingone_davinci_flow.tf"]...)
	repo["definitions/pingone_davinci_connector_instance.tf"] = append(repo["definitions/pingone_davinci_connector_instance.tf"],
		[]byte("\n# Hand-maintained trailing note: the Http connector is shared by Flow A and the subflows.\n")...)
	dir := filepath.Join("testdata", "repo")
	os.RemoveAll(dir)
	writeTree(t, dir, repo)
}

// blockRanges maps addresses to their line ranges in a file.
func blockLines(t *testing.T, name string, src []byte) map[string][2]int {
	t.Helper()
	blocks, err := hclblocks.Scan(src, name)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]int{}
	for _, b := range blocks {
		start := bytes.Count(src[:b.Start], []byte("\n")) + 1
		end := bytes.Count(src[:b.End], []byte("\n")) + 1
		key := b.Address()
		if key == "" {
			key = b.Key() + "@" + path.Base(name)
		}
		out[key] = [2]int{start, end}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// referenceRoot is testdata/repo after the Flow B request: the content of
// terraform/root (the reference root).
func referenceRoot(t *testing.T) MapRepo {
	t.Helper()
	head := readTree(t, filepath.Join("testdata", "repo"))
	exp := neutralized(t, exportertest.FixtureDir("dev"), "pingone_davinci_flow.pingcli__Flow-0020-B")
	res, err := Merge(MergeRequest{Export: exp, Head: head, SourceEnv: "dev", Rollout: []string{"qa", "stage", "prod"},
		Selected: []string{"pingone_davinci_flow.pingcli__Flow-0020-B", "pingone_davinci_flow_deploy.pingcli__Flow-0020-B", "pingone_davinci_flow_enable.pingcli__Flow-0020-B"}})
	if err != nil {
		t.Fatal(err)
	}
	return apply(head, res)
}

const referenceRootDir = "../../terraform/root"

// TestReferenceRootIsCurrent keeps terraform/root in step with the merge:
// it must equal the fixture repository after the Flow B request.
// PINGONE_PROMOTE_REGEN_FIXTURE=1 rewrites it.
func TestReferenceRootIsCurrent(t *testing.T) {
	want := referenceRoot(t)
	if os.Getenv("PINGONE_PROMOTE_REGEN_FIXTURE") != "" {
		writeTree(t, referenceRootDir, want)
	}
	for _, p := range sortedKeys(want) {
		got, err := os.ReadFile(filepath.Join(referenceRootDir, filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(got, want[p]) {
			t.Fatalf("terraform/root/%s is stale; run PINGONE_PROMOTE_REGEN_FIXTURE=1 go test ./internal/promote -run ReferenceRoot", p)
		}
	}
}
