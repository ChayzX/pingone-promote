package multiexport

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/exporter"
)

// TestLiveQuickList reads a real environment (read-only): the time until
// the item list is ready, the time until the terraformer export is in,
// and how the quick DaVinci list compares with the export's. Set
// PINGONE_PROMOTE_LIVE_ENV and PINGONE_PROMOTE_REAL_TERRAFORMER.
func TestLiveQuickList(t *testing.T) {
	path, bin := os.Getenv("PINGONE_PROMOTE_LIVE_ENV"), os.Getenv("PINGONE_PROMOTE_REAL_TERRAFORMER")
	if path == "" || bin == "" {
		t.Skip("set PINGONE_PROMOTE_LIVE_ENV and PINGONE_PROMOTE_REAL_TERRAFORMER to read a real environment")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, x, ok := strings.Cut(sc.Text(), "="); ok {
			v[k] = x
		}
	}
	f.Close()
	c := exporter.Credentials{ClientID: v["PINGONE_PROMOTE_WORKER_CLIENT_ID"], ClientSecret: v["PINGONE_PROMOTE_WORKER_CLIENT_SECRET"], WorkerEnvironmentID: v["PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID"], ExportEnvironmentID: v["PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID"], RegionCode: v["PINGONE_PROMOTE_REGION_CODE"]}
	e := &Exporter{DaVinci: &exporter.Runner{Config: exporter.Config{Path: bin, Version: "0.6.0"}}}
	start := time.Now()
	items, err := e.CatalogAllIDs(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("item list ready in %s: %d items", time.Since(start).Round(time.Millisecond), len(items))
	if p := e.DaVinciProblem(c); p != nil {
		t.Fatalf("quick DaVinci list: %v", p)
	}
	e.mu.Lock()
	cc := e.cache[c.ExportEnvironment()]
	e.mu.Unlock()
	if err := cc.awaitDaVinci(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("terraformer export in after %s", time.Since(start).Round(time.Second))
	if cc.dvErr != nil {
		t.Fatalf("terraformer: %v", cc.dvErr)
	}
	byID := map[string]string{}
	for _, r := range cc.dv {
		byID[r.Type+" "+r.ID] = r.String()
	}
	fast := map[string]bool{}
	same, relabel, missing := 0, 0, 0
	for _, r := range cc.dvFast {
		fast[r.Type+" "+r.ID] = true
		switch to, ok := byID[r.Type+" "+r.ID]; {
		case !ok:
			missing++
			t.Logf("in the quick list, not in the export: %s (ID %s)", r, r.ID)
		case to != r.String():
			relabel++
			t.Logf("label differs: quick %s, export %s", r, to)
		default:
			same++
		}
	}
	extra := 0
	for k, a := range byID {
		if !fast[k] {
			extra++
			t.Logf("in the export, not in the quick list: %s", a)
		}
	}
	t.Logf("quick list vs export: %d same, %d relabelled (matched by ID), %d only in the quick list, %d only in the export", same, relabel, missing, extra)
	if missing+extra > 0 {
		t.Errorf("the quick list and the export disagree on %d items", missing+extra)
	}
	// A refresh with nothing changed reuses the export.
	start = time.Now()
	if _, err := e.Refresh(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	cc2 := e.cache[c.ExportEnvironment()]
	e.mu.Unlock()
	t.Logf("refresh in %s; terraformer reused: %v", time.Since(start).Round(time.Millisecond), cc2.dvDone() && cc2.dvSrc == cc.dvSrc)
}
