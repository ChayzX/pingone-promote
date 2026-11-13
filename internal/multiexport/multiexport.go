// Package multiexport combines the two exporters behind the request
// engine's Exporter interface (ADR 0019): DaVinci types come from the
// pinned pingcli-terraformer, every other PingOne type from the native
// read-only API exporter. It follows references across both, in both
// directions (a core flow-policy assignment references a DaVinci flow
// policy; a DaVinci connector references a core population), so "include
// upstream dependencies" works on the whole graph, and it merges both
// outputs into one terraformer-shaped export for the neutralizer.
package multiexport

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/secretscan"
)

// DaVinci is the terraformer runner's surface (*exporter.Runner).
type DaVinci interface {
	Catalog(ctx context.Context, creds exporter.Credentials, withIDs bool) ([]exporter.Resource, error)
	CatalogAllIDs(ctx context.Context, creds exporter.Credentials) ([]exporter.Resource, error)
	ScopedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error)
	SelectedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error)
	WholeExport(ctx context.Context, creds exporter.Credentials, exclude []string) (*exporter.Export, error)
}

// Exporter is the combined exporter.
type Exporter struct {
	// DaVinci may be nil when the environment has no DaVinci.
	DaVinci DaVinci
	// NewReader builds the core API reader (default: pingone.New).
	NewReader func(exporter.Credentials) (coreexport.Reader, error)
	// CacheFor keeps one environment's inventory and DaVinci catalog, so
	// previews reuse the read behind the item list (default 30m, as long as
	// the server keeps the list). Forget drops it when the list is read
	// again.
	CacheFor time.Duration

	mu    sync.Mutex
	cache map[string]*cached
	// reading holds the read in progress per environment: callers that
	// arrive meanwhile wait for it instead of reading the source again.
	reading map[string]*reading
	// clients keeps the default reader per worker, so its access token
	// and connections carry over from one read to the next.
	clients map[exporter.Credentials]coreexport.Reader
}

// reader returns the core API reader for the worker.
func (e *Exporter) reader(c exporter.Credentials) (coreexport.Reader, error) {
	if e.NewReader != nil {
		return e.NewReader(c)
	}
	key := c
	key.ExportEnvironmentID = "" // one client reads every environment the worker may
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.clients[key]; ok {
		return r, nil
	}
	r, err := DefaultReader(c)
	if err != nil {
		return nil, err
	}
	if e.clients == nil {
		e.clients = map[exporter.Credentials]coreexport.Reader{}
	}
	e.clients[key] = r
	return r, nil
}

type reading struct {
	done chan struct{}
	c    *cached
	err  error
}

type cached struct {
	at  time.Time
	inv *coreexport.Inventory
	dv  []exporter.Resource
	// dvSrc is the whole DaVinci export behind dv, when the DaVinci
	// exporter keeps one: selections are cut from it instead of running
	// terraformer again.
	dvSrc *exporter.Source
	// dvAt is when DaVinci was last read (a failure is retried after
	// dvRetryAfter without reading the core again).
	dvAt time.Time
	// dvReady closes when the terraformer export is in; dv, dvSrc, dvErr
	// and dvExportAt are set before. dvFast is the quick list from the
	// API (davinciList) with its fingerprint dvFP, for the item list
	// meanwhile.
	dvReady    chan struct{}
	dvExportAt time.Time
	dvFast     []exporter.Resource
	dvFP       string
	dvFastErr  error
	// dvErr is why DaVinci could not be read (an environment without
	// DaVinci, or a worker without the DaVinci role). Core items still work.
	dvErr error
}

// DefaultReader reads the real PingOne API.
func DefaultReader(c exporter.Credentials) (coreexport.Reader, error) {
	return pingone.New(pingone.Credentials{ClientID: c.ClientID, ClientSecret: c.ClientSecret, WorkerEnvironmentID: c.WorkerEnvironmentID, RegionCode: c.RegionCode})
}

func (e *Exporter) state(ctx context.Context, creds exporter.Credentials) (*cached, error) {
	return e.get(ctx, creds, false)
}

// readTimeout bounds one read of a source.
const readTimeout = 20 * time.Minute

// get returns the cached read of the source, reading it when there is none
// or it is older than CacheFor (force: always read). Callers that arrive
// during a read wait for it. The read runs on its own, detached from the
// caller that started it, so one person cancelling a preview does not
// fail another's item list; a caller that leaves stops waiting only. A
// failed read keeps the previous one. When only DaVinci failed (an
// environment without it, a missing role), only DaVinci is read again.
func (e *Exporter) get(ctx context.Context, creds exporter.Credentials, force bool) (*cached, error) {
	env := creds.ExportEnvironment()
	ttl := e.CacheFor
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	e.mu.Lock()
	// Reads nobody asked for within ttl are dropped: the cached DaVinci
	// export holds values, which are not kept longer than needed.
	for k, old := range e.cache {
		if k != env && time.Since(old.at) >= ttl {
			delete(e.cache, k)
		}
	}
	c := e.cache[env]
	coreFresh := c != nil && time.Since(c.at) < ttl
	if !force && coreFresh && (!c.dvDone() || c.dvErr == nil || time.Since(c.dvAt) < dvRetryAfter) {
		e.mu.Unlock()
		return c, nil
	}
	rd := e.reading[env]
	if rd == nil {
		var keep *cached
		if !force && coreFresh {
			keep = c // only DaVinci failed: read it again, keep the core read
		}
		rd = &reading{done: make(chan struct{})}
		if e.reading == nil {
			e.reading = map[string]*reading{}
		}
		e.reading[env] = rd
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		go func() {
			defer cancel()
			rd.c, rd.err = e.read(rctx, creds, env, keep, c)
			e.mu.Lock()
			if rd.err == nil {
				if e.cache == nil {
					e.cache = map[string]*cached{}
				}
				e.cache[env] = rd.c
			}
			delete(e.reading, env)
			e.mu.Unlock()
			close(rd.done)
		}()
	}
	e.mu.Unlock()
	select {
	case <-rd.done:
		return rd.c, rd.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dvReuseFor bounds how long an unchanged DaVinci export is reused.
const dvReuseFor = 2 * time.Hour

// read reads the source environment. The core inventory and the quick
// DaVinci list (davinciList) are read at the same time and returned; the
// terraformer export runs on in the background (dvReady closes when it is
// in), unless the quick list shows DaVinci unchanged since prev's export,
// which is then reused. Without a quick list (no DaVinci items, or the API
// refused it) read waits for terraformer. With keep, the core read is
// reused.
func (e *Exporter) read(ctx context.Context, creds exporter.Credentials, env string, keep, prev *cached) (*cached, error) {
	c := &cached{at: time.Now(), dvReady: make(chan struct{})}
	if keep != nil {
		c.at, c.inv = keep.at, keep.inv
	}
	if e.DaVinci == nil {
		close(c.dvReady)
	}
	r, err := e.reader(creds)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	if e.DaVinci != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.dvFast, c.dvFP, c.dvFastErr = davinciList(ctx, r, env)
		}()
	}
	if keep == nil {
		c.inv, err = coreexport.Read(ctx, r, env)
	}
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if e.DaVinci == nil {
		return c, nil
	}
	quick := c.dvFastErr == nil && len(c.dvFast) > 0
	if quick && prev != nil && prev.dvDone() && prev.dvErr == nil && prev.dvSrc != nil && prev.dvFP == c.dvFP && time.Since(prev.dvExportAt) < dvReuseFor {
		// DaVinci is unchanged: no terraformer run.
		c.dv, c.dvSrc, c.dvExportAt, c.dvAt = prev.dv, prev.dvSrc, prev.dvExportAt, prev.dvAt
		close(c.dvReady)
		return c, nil
	}
	c.dvAt = time.Now()
	dvCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	go func() {
		defer cancel()
		defer close(c.dvReady)
		defer func() {
			if p := recover(); p != nil {
				c.dv, c.dvSrc, c.dvErr = nil, nil, fmt.Errorf("multiexport: reading DaVinci failed unexpectedly: %v", p)
			}
		}()
		if cs, ok := e.DaVinci.(interface {
			CatalogSource(context.Context, exporter.Credentials) ([]exporter.Resource, *exporter.Source, error)
		}); ok {
			c.dv, c.dvSrc, c.dvErr = cs.CatalogSource(dvCtx, creds)
		} else {
			c.dv, c.dvErr = e.DaVinci.CatalogAllIDs(dvCtx, creds)
		}
		if c.dvErr != nil {
			c.dv, c.dvSrc = nil, nil
		}
		c.dvExportAt = time.Now()
	}()
	if !quick {
		select {
		case <-c.dvReady:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c, nil
}

// dvDone reports whether the DaVinci export is in (dv, dvSrc and dvErr
// may be read only then).
func (c *cached) dvDone() bool {
	select {
	case <-c.dvReady:
		return true
	default:
		return false
	}
}

// awaitDaVinci waits for the DaVinci export.
func (c *cached) awaitDaVinci(ctx context.Context) error {
	select {
	case <-c.dvReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Refresh reads the source again and returns its item list; until the new
// read is in, previews keep using the previous one (the server's "read
// again" and its periodic refresh).
func (e *Exporter) Refresh(ctx context.Context, creds exporter.Credentials) ([]exporter.Resource, error) {
	c, err := e.get(ctx, creds, true)
	if err != nil {
		return nil, err
	}
	return listOf(c), nil
}

// Catalog lists every exportable resource of the source (DaVinci and core)
// with names and IDs.
func (e *Exporter) Catalog(ctx context.Context, creds exporter.Credentials, _ bool) ([]exporter.Resource, error) {
	c, err := e.state(ctx, creds)
	if err != nil {
		return nil, err
	}
	return listOf(c), nil
}

func listOf(c *cached) []exporter.Resource {
	dv := c.dvFast
	if c.dvDone() && (c.dvErr == nil || len(dv) == 0) {
		dv = c.dv // the export is in: its list is authoritative
	}
	out := append([]exporter.Resource(nil), dv...)
	for _, o := range c.inv.Objects {
		d, _ := catalog.Get(o.Type)
		id := o.ID
		if d.Path == "" || d.Singleton {
			id = "" // derived objects and singletons are not searched by ID
		}
		out = append(out, exporter.Resource{Address: exporter.Address{Type: o.Type, Label: o.Label}, Name: o.Name, ID: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// DaVinciPending reports whether a DaVinci export behind a cached read is
// still running (an export of that source waits for it).
func (e *Exporter) DaVinciPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.cache {
		if !c.dvDone() {
			return true
		}
	}
	return false
}

// DaVinciProblem is why the latest read of the source left out its DaVinci
// items (nil when they were read). It never reads the source itself.
func (e *Exporter) DaVinciProblem(creds exporter.Credentials) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.cache[creds.ExportEnvironment()]; c != nil {
		if !c.dvDone() {
			return c.dvFastErr // the export is still running
		}
		return c.dvErr
	}
	return nil
}

// CatalogAllIDs is Catalog (IDs are always read).
func (e *Exporter) CatalogAllIDs(ctx context.Context, creds exporter.Credentials) ([]exporter.Resource, error) {
	return e.Catalog(ctx, creds, true)
}

// IsDaVinci reports types exported by terraformer.
func IsDaVinci(addr string) bool { return strings.HasPrefix(addr, "pingone_davinci_") }

// matches compares exclusion patterns: an exact address, or a glob. Core
// labels are case-sensitive; terraformer's own matching of DaVinci patterns
// is case-insensitive (ADR 0015), so DaVinci addresses compare that way.
func matches(patterns []string, addr string) bool {
	for _, p := range patterns {
		if p == addr {
			return true
		}
		if IsDaVinci(addr) {
			if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(addr)); ok {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p, addr); ok {
			return true
		}
	}
	return false
}

// index maps every known ID to its address. A DaVinci flow policy's
// catalog ID is "<application>/<policy>"; the policy ID alone is indexed
// too because core resources refer to it that way.
func index(c *cached) map[string]string {
	ids := map[string]string{}
	for _, r := range c.dv {
		if r.ID == "" {
			continue
		}
		ids[r.ID] = r.String()
		if i := strings.LastIndex(r.ID, "/"); i >= 0 {
			ids[r.ID[i+1:]] = r.String()
		}
	}
	for _, o := range c.inv.Objects {
		d, _ := catalog.Get(o.Type)
		if o.ID == "" || d.Path == "" || d.Singleton {
			continue
		}
		if _, dup := ids[o.ID]; !dup {
			ids[o.ID] = o.Address()
		}
	}
	return ids
}

// deps lists the addresses a core object refers to: its parent and every
// string in its API object that is a known ID.
func deps(o *coreexport.Object, ids map[string]string) []string {
	seen := map[string]bool{}
	var walk func(v any, top bool)
	walk = func(v any, top bool) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				// The object's own ID is not a reference; nested {"id": ...}
				// objects are.
				if k == "_links" || k == "environment" || (top && k == "id" && !idIsReference(o)) {
					continue
				}
				walk(vv, false)
			}
		case []any:
			for _, vv := range x {
				walk(vv, false)
			}
		case string:
			if a, ok := ids[x]; ok && a != o.Address() {
				seen[a] = true
			}
		}
	}
	walk(o.Raw, true)
	if o.Parent != nil {
		if a, ok := ids[o.Parent.ID]; ok && a != o.Address() {
			seen[a] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// davinciID matches a DaVinci identifier (PingOne core IDs are UUIDs).
var davinciID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// dvRetryAfter is how long a failed DaVinci read is remembered.
const dvRetryAfter = 15 * time.Second

var quotedUUID = regexp.MustCompile(`"([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})"`)

// coreRefsIn lists the core addresses whose IDs appear as whole string
// literals in DaVinci HCL.
func coreRefsIn(exp *exporter.Export, ids map[string]string) []string {
	seen := map[string]bool{}
	for p, src := range exp.Files {
		if path.Dir(p) != exp.ModuleDir || !strings.HasSuffix(p, ".tf") {
			continue
		}
		for _, m := range quotedUUID.FindAllSubmatch(src, -1) {
			if a, ok := ids[string(m[1])]; ok && !IsDaVinci(a) {
				seen[a] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// ScopedExport exports the seeds and everything they reference,
// transitively, across DaVinci and core.
func (e *Exporter) ScopedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	return e.export(ctx, creds, seeds, exclude, true, false)
}

// SelectedExport exports only the seeds (with their owned children, such
// as a flow's deploy and enable or a sign-on policy's actions).
func (e *Exporter) SelectedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	return e.export(ctx, creds, seeds, exclude, false, false)
}

// WholeExport exports every exportable resource minus the exclusions.
func (e *Exporter) WholeExport(ctx context.Context, creds exporter.Credentials, exclude []string) (*exporter.Export, error) {
	return e.export(ctx, creds, nil, exclude, false, true)
}

func (e *Exporter) export(ctx context.Context, creds exporter.Credentials, seeds, exclude []string, upstream, whole bool) (*exporter.Export, error) {
	if !whole && len(seeds) == 0 {
		return nil, errors.New("multiexport: nothing selected")
	}
	c, err := e.state(ctx, creds)
	if err != nil {
		return nil, err
	}
	// An export needs the DaVinci export (core objects can refer to
	// DaVinci items by ID too). Items chosen from the quick list are
	// matched to it by ID.
	if err := c.awaitDaVinci(ctx); err != nil {
		return nil, err
	}
	seeds, exclude = toExport(seeds, c.dvFast, c.dv), toExport(exclude, c.dvFast, c.dv)
	ids := index(c)
	core, dv := map[string]bool{}, map[string]bool{}
	excluded := func(a string) bool { return matches(exclude, a) }
	// An excluded parent takes its owned children with it.
	excludedObj := func(o *coreexport.Object) bool {
		for p := o; p != nil; p = p.Parent {
			if p.Label != "" && excluded(p.Address()) {
				return true
			}
		}
		return false
	}
	// Built-in objects (default policies, system resources, keys, core
	// mappings) exist in every environment. As with terraformer, an
	// export brings along only what can be created elsewhere: a built-in
	// object is copied when it is selected itself, and otherwise stays a
	// placeholder ID variable for each environment.
	builtin := func(a string) bool {
		o := c.inv.Object(a)
		return o != nil && o.Builtin && (o.Parent == nil || !o.Parent.Builtin || !core[o.Parent.Address()])
	}
	add := func(a string) bool {
		if excluded(a) || builtin(a) {
			return false
		}
		set := core
		if IsDaVinci(a) {
			set = dv
		}
		if set[a] {
			return false
		}
		if !IsDaVinci(a) && c.inv.Object(a) == nil {
			return false
		}
		set[a] = true
		return true
	}
	for _, s := range seeds {
		if IsDaVinci(s) && c.dvErr != nil {
			return nil, fmt.Errorf("multiexport: DaVinci items cannot be read from this environment: %w", c.dvErr)
		}
		if excluded(s) {
			return nil, fmt.Errorf("multiexport: %s is both selected and excluded", s)
		}
		if !IsDaVinci(s) && c.inv.Object(s) == nil {
			return nil, fmt.Errorf("multiexport: %s is not in the source environment", s)
		}
		if !IsDaVinci(s) {
			core[s] = true // selected: copied even when built in
			continue
		}
		add(s)
	}
	if whole {
		for _, o := range c.inv.Objects {
			if !excludedObj(o) && !o.Builtin {
				core[o.Address()] = true
			}
		}
	}
	var dvExp *exporter.Export
	dvKey := ""
	// Each round adds at least one object, so the loop ends; the bound is a
	// guard against a bug, not a limit on dependency depth.
	maxRounds := len(c.inv.Objects) + len(c.dv) + 2
	converged := false
	for round := 0; round < maxRounds; round++ {
		changed := false
		// Owned children travel with their parent.
		for _, o := range c.inv.Objects {
			if o.Parent == nil || o.Parent.Label == "" || !core[o.Parent.Address()] || excludedObj(o) {
				continue
			}
			if d, _ := catalog.Get(o.Type); d.Owned && add(o.Address()) {
				changed = true
			}
		}
		if upstream {
			for _, a := range sortedKeys(core) {
				for _, dep := range deps(c.inv.Object(a), ids) {
					if add(dep) {
						changed = true
					}
				}
			}
		}
		if e.DaVinci != nil && c.dvErr == nil && (whole || len(dv) > 0) {
			key := strings.Join(sortedKeys(dv), ",")
			if dvExp == nil || key != dvKey {
				dvKey = key
				var dvExclude []string
				for _, p := range exclude {
					if typ, _, _ := strings.Cut(p, "."); IsDaVinci(typ + ".") {
						dvExclude = append(dvExclude, p)
					}
				}
				switch {
				case c.dvSrc != nil && whole:
					dvExp, err = c.dvSrc.Whole(dvExclude)
				case c.dvSrc != nil && upstream:
					dvExp, err = c.dvSrc.Scoped(sortedKeys(dv), dvExclude)
				case c.dvSrc != nil:
					dvExp, err = c.dvSrc.Selected(sortedKeys(dv), dvExclude)
				case whole:
					dvExp, err = e.DaVinci.WholeExport(ctx, creds, dvExclude)
				case upstream:
					dvExp, err = e.DaVinci.ScopedExport(ctx, creds, sortedKeys(dv), dvExclude)
				default:
					dvExp, err = e.DaVinci.SelectedExport(ctx, creds, sortedKeys(dv), dvExclude)
				}
				if err != nil {
					return nil, err
				}
				if upstream || whole {
					for _, a := range coreRefsIn(dvExp, ids) {
						if add(a) {
							changed = true
						}
					}
				}
			}
		} else if e.DaVinci == nil && len(dv) > 0 {
			return nil, fmt.Errorf("multiexport: %s needs the DaVinci exporter, which is not configured", sortedKeys(dv)[0])
		}
		if !changed {
			converged = true
			break
		}
	}
	if !converged {
		return nil, errors.New("multiexport: following dependencies did not converge")
	}
	exported := map[string]bool{}
	for a := range core {
		exported[a] = true
	}
	if dvExp != nil {
		have, err := exporter.Addresses(dvExp)
		if err != nil {
			return nil, err
		}
		for _, a := range have {
			exported[a] = true
		}
	}
	rendered, err := coreexport.Render(coreexport.RenderInput{Inventory: c.inv, Addresses: sortedKeys(core), Exported: exported, IDs: ids,
		ModuleDir: exporter.ModuleDir, ModuleName: exporter.ModuleName})
	if err != nil {
		return nil, err
	}
	if c.dvErr != nil {
		// A DaVinci ID that a core object refers to would be written as a
		// literal source ID; refuse rather than commit it.
		for _, k := range sortedKeysS(rendered.Unresolved) {
			if davinciID.MatchString(rendered.Unresolved[k]) {
				return nil, fmt.Errorf("multiexport: %s refers to a DaVinci item, and DaVinci could not be read: %w", k, c.dvErr)
			}
		}
	}
	out, err := combine(c, dvExp, rendered, ids, exported, whole)
	if err != nil {
		return nil, err
	}
	out.Owners = map[string]string{}
	for a := range core {
		o := c.inv.Object(a)
		if d, _ := catalog.Get(o.Type); d.Owned && o.Parent != nil && o.Parent.Label != "" && core[o.Parent.Address()] {
			out.Owners[a] = o.Parent.Address()
		}
	}
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const envIDVariable = `variable "pingone_environment_id" {
  type        = string
  description = "The PingOne environment ID to configure resources in"
}
`

// aboutExported reports whether a source-read note names a type that the
// rendered core objects include. Notes that name no type are kept.
func aboutExported(w string, core *coreexport.Rendered) bool {
	typ := typeRE.FindString(w)
	if typ == "" {
		return true
	}
	for p := range core.Files {
		if strings.HasSuffix(p, "/"+typ+".tf") {
			return true
		}
	}
	return false
}

var typeRE = regexp.MustCompile(`pingone_[a-z0-9_]+`)

// combine merges the DaVinci export and the rendered core objects. Core IDs
// that appear as whole string literals inside DaVinci blocks become
// references (or ID variables when the core object is not exported).
func combine(c *cached, dvExp *exporter.Export, core *coreexport.Rendered, ids map[string]string, exported map[string]bool, whole bool) (*exporter.Export, error) {
	out := &exporter.Export{EnvironmentID: c.inv.EnvironmentID, ModuleDir: exporter.ModuleDir, ModuleName: exporter.ModuleName,
		Files: map[string][]byte{}, Referenced: map[string]string{}, Adopt: core.Adopt}
	// Notes from reading the source are shown for a whole environment, and
	// otherwise only for the types in this export (a label clash among role
	// assignments is no concern of a request for one flow).
	for _, w := range c.inv.Warnings {
		if whole || aboutExported(w, core) {
			out.Warnings = append(out.Warnings, w)
		}
	}
	if c.dvErr != nil {
		why := "the DaVinci export failed"
		var ex *exporter.ExecError
		if errors.As(c.dvErr, &ex) {
			why = "the DaVinci export tool failed (the service log has the details)"
		}
		out.Warnings = append(out.Warnings, "DaVinci items were not read: "+why+". If the source environment has DaVinci, check the worker's DaVinci role and try again; only core PingOne items are in this export")
	}
	if len(core.Unresolved) > 0 {
		keys := sortedKeysS(core.Unresolved)
		more := ""
		if len(keys) > 5 {
			keys, more = keys[:5], fmt.Sprintf(" and %d more", len(core.Unresolved)-5)
		}
		why := "items this tool does not copy, such as images and PingOne's built-in roles"
		if len(c.inv.Skipped) > 0 {
			why += ", or items of a type that was not read"
		}
		out.Warnings = append(out.Warnings, "These values look like IDs of "+why+", so they keep the source environment's value: "+strings.Join(keys, ", ")+more+". Check that each is the same in every environment before merging")
	}
	for _, t := range sortedKeysS(c.inv.Skipped) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s was not read: %s", t, c.inv.Skipped[t]))
	}
	for a, id := range core.Referenced {
		out.Referenced[a] = id
	}
	varsPath := exporter.ModuleDir + "/variables.tf"
	var extraVars []byte
	if dvExp != nil {
		for p, b := range dvExp.Files {
			out.Files[p] = b
		}
		out.Findings = append(out.Findings, dvExp.Findings...)
		// Core IDs inside DaVinci blocks (sorted, so the generated
		// variables come out in a stable order).
		paths := make([]string, 0, len(dvExp.Files))
		for p := range dvExp.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			src := dvExp.Files[p]
			if path.Dir(p) != dvExp.ModuleDir || !strings.HasSuffix(p, ".tf") || path.Base(p) == "variables.tf" {
				continue
			}
			out.Files[p] = replaceUUIDValues(src, func(m []byte) []byte {
				id := string(m[1 : len(m)-1])
				a, ok := ids[id]
				if !ok || IsDaVinci(a) {
					return m
				}
				if exported[a] {
					return []byte(a + ".id")
				}
				name := neutralize.ExcludedVariableName(a)
				if _, done := out.Referenced[a]; !done {
					typ, _, _ := strings.Cut(a, ".")
					extraVars = append(extraVars, []byte(fmt.Sprintf("variable %q {\n  type        = string\n  description = %q\n}\n", name, "ID of "+typ+" resource (excluded from export)"))...)
				}
				out.Referenced[a] = id
				return []byte("var." + name)
			})
		}
	} else {
		out.Files[varsPath] = []byte(envIDVariable)
	}
	for p, b := range core.Files {
		switch {
		case p == varsPath:
			extraVars = append(extraVars, b...)
		default:
			if _, clash := out.Files[p]; clash {
				return nil, fmt.Errorf("multiexport: both exporters wrote %s", p)
			}
			out.Files[p] = b
		}
	}
	merged, err := mergeVariables(out.Files[varsPath], extraVars)
	if err != nil {
		return nil, err
	}
	out.Files[varsPath] = merged
	// The DaVinci source values for core ID variables live in the core
	// values file already; add the ones only DaVinci blocks need.
	var vals strings.Builder
	for _, a := range sortedKeysS(out.Referenced) {
		if _, fromCore := core.Referenced[a]; fromCore {
			continue
		}
		fmt.Fprintf(&vals, "%s = %q\n", neutralize.ExcludedVariableName(a), out.Referenced[a])
	}
	if vals.Len() > 0 {
		out.Files["ping-export-cross.auto.tfvars"] = []byte(vals.String())
	}
	for p, b := range core.Files {
		if strings.HasSuffix(p, ".tf") {
			out.Findings = append(out.Findings, secretscan.ScanFiles(map[string][]byte{p: b})...)
		}
	}
	out.Warnings = append(out.Warnings, core.Warnings...)
	return out, nil
}

func sortedKeysS(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeVariables appends variable blocks whose names are not declared yet.
func mergeVariables(base, extra []byte) ([]byte, error) {
	have := map[string]bool{}
	blocks, err := hclblocks.Scan(base, "variables.tf")
	if err != nil {
		return nil, err
	}
	for _, b := range blocks {
		if b.Type == "variable" && len(b.Labels) == 1 {
			have[b.Labels[0]] = true
		}
	}
	more, err := hclblocks.Scan(extra, "variables.tf")
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), base...)
	for _, b := range more {
		if b.Type != "variable" || len(b.Labels) != 1 || have[b.Labels[0]] {
			continue
		}
		have[b.Labels[0]] = true
		out = append(out, '\n')
		out = append(out, b.Text(extra)...)
		out = append(out, '\n')
	}
	return out, nil
}

// Verify checks the pinned DaVinci exporter (serve --check-config).
func (e *Exporter) Verify(ctx context.Context) error {
	if v, ok := e.DaVinci.(interface{ Verify(context.Context) error }); ok {
		return v.Verify(ctx)
	}
	return nil
}

// replaceUUIDValues rewrites quoted UUIDs that are values, leaving object
// keys ("<uuid>" = ... or "<uuid>" : ...) alone.
func replaceUUIDValues(src []byte, f func([]byte) []byte) []byte {
	var out []byte
	last := 0
	for _, loc := range quotedUUID.FindAllIndex(src, -1) {
		rest := src[loc[1]:]
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		if i < len(rest) && (rest[i] == '=' || rest[i] == ':') && !(i+1 < len(rest) && rest[i] == '=' && rest[i+1] == '=') {
			continue
		}
		out = append(out, src[last:loc[0]]...)
		out = append(out, f(src[loc[0]:loc[1]])...)
		last = loc[1]
	}
	return append(out, src[last:]...)
}

// idIsReference reports types whose "id" field names another object (a
// group nesting's item is the containing group).
func idIsReference(o *coreexport.Object) bool {
	d, ok := catalog.Get(o.Type)
	if !ok {
		return false
	}
	for _, v := range d.Rename {
		if v == "id" {
			return true
		}
	}
	return false
}
