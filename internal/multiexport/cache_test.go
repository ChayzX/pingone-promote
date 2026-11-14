package multiexport

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/coreexport/schematenant"
	"pingone-promote/internal/exporter"
)

// slowReader counts reads and can hold them until released.
type slowReader struct {
	coreexport.Reader
	gate  chan struct{}
	lists atomic.Int32
}

func (s *slowReader) List(ctx context.Context, env, path, embedded string) ([]map[string]any, error) {
	switch {
	case path == "flows" || path == "variables" || path == "connectorInstances" || strings.HasPrefix(path, "davinciApplications"):
		// the quick DaVinci list, not the core read
	default:
		s.lists.Add(1)
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Reader.List(ctx, env, path, embedded)
}

// flakyDaVinci fails until ok is set.
type flakyDaVinci struct {
	DaVinci
	calls atomic.Int32
	ok    atomic.Bool
}

func (f *flakyDaVinci) CatalogAllIDs(context.Context, exporter.Credentials) ([]exporter.Resource, error) {
	f.calls.Add(1)
	if f.ok.Load() {
		return nil, nil
	}
	return nil, errors.New("no DaVinci role")
}

// A caller that gives up does not fail another caller waiting for the same
// read.
func TestCancelledCallerDoesNotFailTheSharedRead(t *testing.T) {
	r := &slowReader{Reader: schematenant.New(), gate: make(chan struct{})}
	e := &Exporter{NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return r, nil }}
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); _, err1 = e.Catalog(ctx1, creds, true) }()
	time.Sleep(50 * time.Millisecond)
	go func() { defer wg.Done(); _, err2 = e.Catalog(context.Background(), creds, true) }()
	time.Sleep(50 * time.Millisecond)
	cancel1()
	close(r.gate)
	wg.Wait()
	if !errors.Is(err1, context.Canceled) {
		t.Fatalf("the caller that left: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("the other caller failed with the first one's cancellation: %v", err2)
	}
}

// A refresh keeps the previous read in use until the new one is in.
func TestRefreshKeepsTheOldReadMeanwhile(t *testing.T) {
	r := &slowReader{Reader: schematenant.New()}
	e := &Exporter{NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return r, nil }}
	if _, err := e.Catalog(context.Background(), creds, true); err != nil {
		t.Fatal(err)
	}
	r.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := e.Refresh(context.Background(), creds); done <- err }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if _, err := e.WholeExport(context.Background(), creds, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("an export waited %s for the refresh", d)
	}
	close(r.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// When only DaVinci fails, only DaVinci is read again.
func TestDaVinciFailureRetriesDaVinciOnly(t *testing.T) {
	r := &slowReader{Reader: schematenant.New()}
	dv := &flakyDaVinci{}
	e := &Exporter{DaVinci: dv, NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return r, nil }}
	if _, err := e.Catalog(context.Background(), creds, true); err != nil {
		t.Fatal(err)
	}
	coreReads := r.lists.Load()
	e.mu.Lock()
	e.cache[creds.ExportEnvironment()].dvAt = time.Now().Add(-time.Minute)
	e.mu.Unlock()
	dv.ok.Store(true)
	if _, err := e.Catalog(context.Background(), creds, true); err != nil {
		t.Fatal(err)
	}
	if dv.calls.Load() != 2 {
		t.Fatalf("DaVinci read %d times, want 2", dv.calls.Load())
	}
	if r.lists.Load() != coreReads {
		t.Fatalf("the core was read again (%d requests after %d)", r.lists.Load(), coreReads)
	}
	if e.DaVinciProblem(creds) != nil {
		t.Fatal("the DaVinci retry was not kept")
	}
}
