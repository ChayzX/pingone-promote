package multiexport

import (
	"context"
	"sync"
	"time"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/neutralize"
)

// Facts is what an environment already has, by address (labels follow
// names, so an item with the same name has the same address in every
// environment). It is read to fill in per-environment values: the ID of
// an existing item, the import ID to adopt one, a DaVinci variable's
// value, an attribute's value.
type Facts struct {
	// IDs are object IDs; ImportIDs the IDs Terraform imports them by.
	IDs, ImportIDs map[string]string
	// Values are DaVinci string variables' values (secrets are masked by
	// the API and left out).
	Values map[string]string
	// Raw are the API objects, for attribute values.
	Raw map[string]map[string]any
	At  time.Time
	// Skipped are kinds of items that could not be read (403: no read
	// role, or a service the environment does not have), and DaVinci is
	// why DaVinci could not be read ("" when it was).
	Skipped map[string]string
	DaVinci string
}

type factsEntry struct {
	ready chan struct{}
	f     *Facts
	err   error
}

// EnvironmentFacts reads what the environment creds.ExportEnvironmentID
// already has, read-only: the core items and the DaVinci list (no
// terraformer run). A read is kept for CacheFor; callers that arrive
// during one wait for it.
func (e *Exporter) EnvironmentFacts(ctx context.Context, creds exporter.Credentials) (*Facts, error) {
	env := creds.ExportEnvironment()
	ttl := e.CacheFor
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	e.mu.Lock()
	if e.facts == nil {
		e.facts = map[string]*factsEntry{}
	}
	ent := e.facts[env]
	fresh := false
	if ent != nil {
		select {
		case <-ent.ready:
			fresh = ent.err == nil && time.Since(ent.f.At) < ttl
		default:
			fresh = true // being read
		}
	}
	if !fresh {
		ent = &factsEntry{ready: make(chan struct{})}
		e.facts[env] = ent
		go func() {
			defer close(ent.ready)
			ent.f, ent.err = e.readFacts(context.WithoutCancel(ctx), creds, env)
		}()
	}
	e.mu.Unlock()
	select {
	case <-ent.ready:
		return ent.f, ent.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Exporter) readFacts(ctx context.Context, creds exporter.Credentials, env string) (*Facts, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	r, err := e.reader(creds)
	if err != nil {
		return nil, err
	}
	f := &Facts{IDs: map[string]string{}, ImportIDs: map[string]string{}, Values: map[string]string{}, Raw: map[string]map[string]any{}, At: time.Now()}
	var dv []exporter.Resource
	var dvValues map[string]string
	var dvRaw map[string]map[string]any
	var dvErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var l dvListing
		l, dvErr = davinciListing(ctx, r, env)
		dv, dvValues, dvRaw = l.items, l.values, l.raw
	}()
	inv, err := coreexport.Read(ctx, r, env)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	for _, o := range inv.Objects {
		a := o.Address()
		if d, _ := catalog.Get(o.Type); o.ID != "" && d.Path != "" && !d.Singleton {
			f.IDs[a] = o.ID
		}
		if o.ImportID != "" {
			f.ImportIDs[a] = o.ImportID
		}
		f.Raw[a] = o.Raw
	}
	f.Skipped = inv.Skipped
	if dvErr != nil {
		f.DaVinci = dvErr.Error()
	}
	if dvErr == nil { // an environment without DaVinci has core facts only
		for _, it := range dv {
			a := it.String()
			f.IDs[a], f.ImportIDs[a] = it.ID, it.ID
			if v, ok := dvValues[it.ID]; ok && it.Type == neutralize.TypeVariable {
				f.Values[a] = v
			}
			if raw, ok := dvRaw[it.ID]; ok {
				f.Raw[a] = raw
			}
		}
	}
	return f, nil
}
