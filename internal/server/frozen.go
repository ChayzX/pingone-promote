package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/request"
)

// frozenExport holds what the exporter returned during a preview. Submit
// replays it instead of exporting again, so the commit contains exactly
// the source configuration that was reviewed.
type frozenExport struct {
	mu    sync.Mutex
	calls map[string]any
}

var errNotFrozen = errors.New("the source export from the preview is not available; preview again")

func callKey(method string, creds exporter.Credentials, args ...any) string {
	b, _ := json.Marshal(append([]any{method, creds.ExportEnvironmentID, creds.WorkerEnvironmentID, creds.RegionCode}, args...))
	return string(b)
}

func copyResources(in []exporter.Resource) []exporter.Resource {
	return append([]exporter.Resource(nil), in...)
}

func copyExport(in *exporter.Export) *exporter.Export {
	if in == nil {
		return nil
	}
	out := *in
	out.Files = make(map[string][]byte, len(in.Files))
	for k, v := range in.Files {
		out.Files[k] = append([]byte(nil), v...)
	}
	out.Findings = append(out.Findings[:0:0], in.Findings...)
	return &out
}

func (f *frozenExport) put(key string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[key] = v
}

func (f *frozenExport) get(key string) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.calls[key]
	return v, ok
}

// recorder exports with the real exporter and keeps a copy of each result.
type recorder struct {
	inner request.Exporter
	f     *frozenExport
}

func newRecorder(inner request.Exporter) (*recorder, *frozenExport) {
	f := &frozenExport{calls: map[string]any{}}
	return &recorder{inner: inner, f: f}, f
}

func (r *recorder) Catalog(ctx context.Context, creds exporter.Credentials, withIDs bool) ([]exporter.Resource, error) {
	out, err := r.inner.Catalog(ctx, creds, withIDs)
	if err == nil {
		r.f.put(callKey("Catalog", creds, withIDs), copyResources(out))
	}
	return out, err
}

func (r *recorder) CatalogAllIDs(ctx context.Context, creds exporter.Credentials) ([]exporter.Resource, error) {
	out, err := r.inner.CatalogAllIDs(ctx, creds)
	if err == nil {
		r.f.put(callKey("CatalogAllIDs", creds), copyResources(out))
	}
	return out, err
}

func (r *recorder) ScopedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	out, err := r.inner.ScopedExport(ctx, creds, seeds, exclude)
	if err == nil {
		r.f.put(callKey("ScopedExport", creds, seeds, exclude), copyExport(out))
	}
	return out, err
}

func (r *recorder) WholeExport(ctx context.Context, creds exporter.Credentials, exclude []string) (*exporter.Export, error) {
	out, err := r.inner.WholeExport(ctx, creds, exclude)
	if err == nil {
		r.f.put(callKey("WholeExport", creds, exclude), copyExport(out))
	}
	return out, err
}

// SelectedExport forwards to an exporter that can export without upstream
// dependencies, and fails like the engine would for one that cannot.
func (r *recorder) SelectedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	sel, ok := r.inner.(request.SelectedExporter)
	if !ok {
		return nil, errors.New("this exporter cannot export without upstream dependencies")
	}
	out, err := sel.SelectedExport(ctx, creds, seeds, exclude)
	if err == nil {
		r.f.put(callKey("SelectedExport", creds, seeds, exclude), copyExport(out))
	}
	return out, err
}

// replayer answers from a frozen export and never contacts PingOne.
type replayer struct{ f *frozenExport }

func (p replayer) resources(key string) ([]exporter.Resource, error) {
	v, ok := p.f.get(key)
	if !ok {
		return nil, errNotFrozen
	}
	return copyResources(v.([]exporter.Resource)), nil
}

func (p replayer) export(key string) (*exporter.Export, error) {
	v, ok := p.f.get(key)
	if !ok {
		return nil, errNotFrozen
	}
	return copyExport(v.(*exporter.Export)), nil
}

func (p replayer) Catalog(_ context.Context, creds exporter.Credentials, withIDs bool) ([]exporter.Resource, error) {
	return p.resources(callKey("Catalog", creds, withIDs))
}

func (p replayer) CatalogAllIDs(_ context.Context, creds exporter.Credentials) ([]exporter.Resource, error) {
	return p.resources(callKey("CatalogAllIDs", creds))
}

func (p replayer) ScopedExport(_ context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	return p.export(callKey("ScopedExport", creds, seeds, exclude))
}

func (p replayer) WholeExport(_ context.Context, creds exporter.Credentials, exclude []string) (*exporter.Export, error) {
	return p.export(callKey("WholeExport", creds, exclude))
}

func (p replayer) SelectedExport(_ context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	return p.export(callKey("SelectedExport", creds, seeds, exclude))
}
