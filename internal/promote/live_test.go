package promote

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/neutralize"
)

// TestLiveTenant reads a real PingOne environment, read-only, and runs
// every check the generated tenants run: the item list, every item alone
// (with and without what it depends on), and a whole-environment copy
// through neutralizer, merge, check --root and terraform validate.
//
//	PINGONE_PROMOTE_LIVE_ENV   file of KEY=VALUE lines: PINGONE_PROMOTE_WORKER_CLIENT_ID,
//	                           _WORKER_CLIENT_SECRET, _WORKER_ENVIRONMENT_ID,
//	                           _SOURCE_ENVIRONMENT_ID, _REGION_CODE
//	PINGONE_PROMOTE_REAL_TERRAFORMER  pingcli-terraformer v0.6.0 (DaVinci)
//
// Credentials are never logged. Only GET requests reach PingOne (the
// client has no other method); the worker should hold read-only roles.
func TestLiveTenant(t *testing.T) {
	path := os.Getenv("PINGONE_PROMOTE_LIVE_ENV")
	if path == "" {
		t.Skip("set PINGONE_PROMOTE_LIVE_ENV to a credentials file to read a real environment")
	}
	vals := readEnvFile(t, path)
	creds := exporter.Credentials{
		ClientID: vals["PINGONE_PROMOTE_WORKER_CLIENT_ID"], ClientSecret: vals["PINGONE_PROMOTE_WORKER_CLIENT_SECRET"],
		WorkerEnvironmentID: vals["PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID"], ExportEnvironmentID: vals["PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID"],
		RegionCode: vals["PINGONE_PROMOTE_REGION_CODE"],
	}
	secret := creds.ClientSecret
	// No message may carry the secret.
	check := func(s string) string {
		if secret != "" && strings.Contains(s, secret) {
			t.Fatal("a message contained the client secret")
		}
		return s
	}
	report := &strings.Builder{}
	logf := func(format string, a ...any) {
		line := check(fmt.Sprintf(format, a...))
		report.WriteString(line + "\n")
		t.Log(line)
	}
	defer func() {
		if p := os.Getenv("PINGONE_PROMOTE_LIVE_REPORT"); p != "" {
			_ = os.WriteFile(p, []byte(report.String()), 0o600)
		}
	}()

	e := &multiexport.Exporter{CacheFor: time.Hour}
	if bin := os.Getenv("PINGONE_PROMOTE_REAL_TERRAFORMER"); bin != "" {
		e.DaVinci = &exporter.Runner{Config: exporter.Config{Path: bin, SHA256: fileSHA(t, bin), Version: "0.6.0"}}
	} else {
		logf("NOTE: PINGONE_PROMOTE_REAL_TERRAFORMER is not set; DaVinci is not read")
	}
	ctx := context.Background()

	// 1. The item list.
	start := time.Now()
	items, err := e.CatalogAllIDs(ctx, creds)
	if err != nil {
		t.Fatal(check(err.Error()))
	}
	logf("item list: %d items, read in %s", len(items), time.Since(start).Round(time.Second))
	if p := e.DaVinciProblem(creds); p != nil {
		t.Errorf("%s", check("DaVinci items are missing: "+p.Error()))
	}
	byType := map[string]int{}
	for _, it := range items {
		byType[it.Type]++
	}
	for _, typ := range sortedKeysI(byType) {
		logf("  %-55s %d", typ, byType[typ])
	}

	// 2. The whole environment through the pipeline.
	t0 := time.Now()
	exp, err := e.WholeExport(ctx, creds, nil)
	if err != nil {
		t.Fatal(check(err.Error()))
	}
	logf("whole environment exported in %s", time.Since(t0).Round(time.Second))
	if dump := os.Getenv("PINGONE_PROMOTE_LIVE_DUMP"); dump != "" {
		// Configuration only: tfvars files (which can hold values) are
		// never written.
		for p, b := range exp.Files {
			if strings.HasSuffix(p, ".tfvars") || !strings.HasSuffix(p, ".tf") {
				continue
			}
			full := filepath.Join(dump, filepath.FromSlash(p))
			_ = os.MkdirAll(filepath.Dir(full), 0o700)
			_ = os.WriteFile(full, []byte(check(string(b))), 0o600)
		}
	}
	for _, w := range exp.Warnings {
		logf("warning: %s", w)
	}
	lift := append([]neutralize.LiftRule(nil), neutralize.DefaultLiftRules...)
	for _, d := range catalog.All() {
		for _, a := range d.EnvLocal {
			lift = append(lift, neutralize.LiftRule{ResourceType: d.Type, Attribute: a})
		}
	}
	var excluded []neutralize.Excluded
	for a, id := range exp.Referenced {
		excluded = append(excluded, neutralize.Excluded{Address: a, ID: id})
	}
	neu, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName},
		neutralize.Options{SourceEnvironmentID: creds.ExportEnvironment(), Excluded: excluded, Lift: lift})
	if err != nil {
		t.Fatal(check(err.Error()))
	}
	for _, w := range neu.Warnings {
		logf("neutralizer: %s", w)
	}
	var perEnv, ids []string
	for name, v := range neu.Variables {
		if v.PerEnvironment() {
			perEnv = append(perEnv, name)
		}
	}
	for a := range exp.Referenced {
		ids = append(ids, a)
	}
	sort.Strings(perEnv)
	sort.Strings(ids)
	logf("readiness: %d values per environment, %d of them IDs of items outside the copy", len(perEnv), len(ids))
	for _, n := range perEnv {
		logf("  value: %s", n)
	}
	for _, a := range ids {
		logf("  outside the copy: %s", a)
	}
	logf("adopted: %d", len(exp.Adopt))
	rv := map[string]map[string]cty.Value{}
	for _, env := range rollout {
		rv[env] = map[string]cty.Value{neutralize.EnvironmentIDVariable: cty.StringVal("22222222-2222-4222-8222-222222222222")}
		for _, name := range perEnv {
			if sv, ok := neu.SourceValues[name]; ok {
				rv[env][name] = sv
			} else {
				// No source value: the person types one on the page.
				rv[env][name] = cty.StringVal("entered-" + env)
			}
		}
	}
	for _, name := range perEnv {
		if _, ok := neu.SourceValues[name]; !ok {
			logf("  value with nothing to prefill (typed for every environment): %s", name)
		}
	}
	rv["dev"] = map[string]cty.Value{}
	for _, name := range perEnv {
		if _, ok := neu.SourceValues[name]; !ok {
			rv["dev"][name] = cty.StringVal("entered-dev")
		}
	}
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	res, err := Merge(MergeRequest{Export: neu, Head: head, WholeEnvironment: true, SourceEnv: "dev", Rollout: rollout, EnvValues: rv})
	if err != nil {
		t.Fatal(check(err.Error()))
	}
	if res.Blocked() {
		for _, b := range res.Blockers {
			t.Errorf("%s", check(fmt.Sprintf("blocker: %+v", b)))
		}
	} else {
		logf("whole environment: %d files, %d secrets", len(res.Files), len(res.RequiredSecrets))
		// check --root, terraform validate (every diagnostic counts: these
		// are real values) and a mock plan for the source environment.
		if t.Run("whole", func(t *testing.T) { endToEndPlans(t, apply(head, res), res.RequiredSecrets, []string{"dev"}) }) {
			logf("whole environment: check --root, terraform validate and the mock plan passed")
		} else {
			logf("whole environment: check --root, terraform validate or the mock plan FAILED (above)")
		}
	}

	// 3. Every core item alone, with and without what it depends on, and
	// a sample of DaVinci items (each of those runs the terraformer).
	failed, done, perDV := 0, 0, map[string]int{}
	budget := time.Now().Add(25 * time.Minute)
	ordered := append([]exporter.Resource(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return !strings.HasPrefix(ordered[i].Type, "pingone_davinci_") && strings.HasPrefix(ordered[j].Type, "pingone_davinci_")
	})
	for _, it := range ordered {
		if strings.HasPrefix(it.Type, "pingone_davinci_") {
			if perDV[it.Type] >= 1 {
				continue
			}
			perDV[it.Type]++
		}
		if time.Now().After(budget) {
			logf("single items: stopped at the time budget after %d", done)
			break
		}
		a := it.Type + "." + it.Label
		for _, up := range []bool{false, true} {
			t0 := time.Now()
			var exp *exporter.Export
			if up {
				exp, err = e.ScopedExport(ctx, creds, []string{a}, nil)
			} else {
				exp, err = e.SelectedExport(ctx, creds, []string{a}, nil)
			}
			if err == nil {
				err = liveNeutralize(exp, creds.ExportEnvironment())
			}
			if err != nil {
				failed++
				t.Errorf("%s", check(fmt.Sprintf("%s (with dependencies %v): %v", a, up, err)))
			}
			if d := time.Since(t0); d > 20*time.Second {
				logf("  slow: %s (with dependencies %v) took %s", a, up, d.Round(time.Second))
			}
		}
		done++
		if done%100 == 0 {
			logf("single items: %d done", done)
		}
	}
	logf("single items: %d items, %d exports failed", done, failed)
}

func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok {
			out[k] = v
		}
	}
	for _, k := range []string{"PINGONE_PROMOTE_WORKER_CLIENT_ID", "PINGONE_PROMOTE_WORKER_CLIENT_SECRET", "PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID", "PINGONE_PROMOTE_REGION_CODE"} {
		if out[k] == "" {
			t.Fatalf("credentials file lacks %s", k)
		}
	}
	return out
}

func fileSHA(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func liveNeutralize(exp *exporter.Export, env string) error {
	var ex []neutralize.Excluded
	for a, id := range exp.Referenced {
		ex = append(ex, neutralize.Excluded{Address: a, ID: id})
	}
	_, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName},
		neutralize.Options{SourceEnvironmentID: env, Excluded: ex})
	return err
}

func sortedKeysI(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
