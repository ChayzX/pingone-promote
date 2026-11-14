package nativepoc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the pipeline's settings (promote.yml in the repository).
type Config struct {
	Region            string            `yaml:"region"` // com, eu, ca, com.au, sg, asia
	WorkerEnvironment string            `yaml:"workerEnvironmentId"`
	Environments      map[string]string `yaml:"environments"` // name -> PingOne environment ID
}

// Request is one promotion asked for in Git (promotions/<ticket>.yml).
type Request struct {
	Ticket      string `yaml:"ticket"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
	Target      string `yaml:"target"`
	Resources   []struct {
		Type string `yaml:"type"`
		Name string `yaml:"name"`
	} `yaml:"resources"`
}

// Load reads a YAML file strictly (an unknown setting is an error).
func Load(path string, into any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (cfg Config) env(name string) (Ref, error) {
	id := cfg.Environments[name]
	if id == "" {
		return Ref{}, fmt.Errorf("environment %q is not in promote.yml", name)
	}
	return Ref{ID: id, Name: name}, nil
}

// Plan drafts the promotion a request asks for and reviews its plan.
// Nothing changes in the target.
func Plan(ctx context.Context, c *Client, cfg Config, req Request) (Review, error) {
	var r Review
	if req.Ticket == "" || len(req.Resources) == 0 {
		return r, errors.New("the request needs a ticket and at least one resource")
	}
	src, err := cfg.env(req.Source)
	if err != nil {
		return r, err
	}
	dst, err := cfg.env(req.Target)
	if err != nil {
		return r, err
	}
	if src.ID == dst.ID {
		return r, errors.New("source and target are the same environment")
	}
	source, err := c.TakeSnapshot(ctx, src.ID)
	if err != nil {
		return r, err
	}
	selected := map[string]bool{}
	var ids []string
	for _, want := range req.Resources {
		var found []string
		for id, res := range source.Resources {
			if res.Name == want.Name && (want.Type == "" || strings.EqualFold(res.Type, want.Type)) {
				found = append(found, id)
			}
		}
		switch len(found) {
		case 0:
			return r, fmt.Errorf("%s has no %s named %q", req.Source, orAny(want.Type), want.Name)
		case 1:
			if !selected[found[0]] {
				selected[found[0]] = true
				ids = append(ids, found[0])
			}
		default:
			return r, fmt.Errorf("%s has %d resources named %q; add its type to the request", req.Source, len(found), want.Name)
		}
	}
	sort.Strings(ids)
	p, err := c.CreatePromotion(ctx, src.ID, dst.ID, source.ID, ids, req.Ticket+": "+req.Description)
	if err != nil {
		return r, err
	}
	r, err = review(ctx, c, src, dst, source, selected, p)
	r.Ticket, r.Description = req.Ticket, req.Description
	return r, err
}

func orAny(t string) string {
	if t == "" {
		return "resource"
	}
	return t
}

func review(ctx context.Context, c *Client, src, dst Ref, source *Snapshot, selected map[string]bool, p Promotion) (Review, error) {
	steps, err := c.Steps(ctx, src.ID, p.ID)
	if err != nil {
		return Review{}, err
	}
	// Compared with the target as it is now, not as it was when the
	// promotion was drafted.
	target, err := c.TakeSnapshot(ctx, dst.ID)
	if err != nil {
		return Review{}, err
	}
	r := BuildReview(source, target, src, dst, selected, p, steps)
	r.View = viewDigest(r.Items)
	for _, it := range r.Items {
		if it.Selected {
			r.Selected = append(r.Selected, it.SourceID)
		}
	}
	return r, nil
}

// viewDigest identifies what a reviewer was shown: every item and field,
// before and after.
func viewDigest(items []Item) string {
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Apply runs a reviewed promotion. It refuses when the promotion's
// operations, or what they would change in the target, are no longer what
// the review showed.
func Apply(ctx context.Context, c *Client, reviewed Review) (Promotion, error) {
	var p Promotion
	steps, err := c.Steps(ctx, reviewed.Source.ID, reviewed.PromotionID)
	if err != nil {
		return p, err
	}
	if Digest(steps) != reviewed.Digest {
		return p, errors.New("the promotion's operations are not the ones that were reviewed; plan again")
	}
	source, err := c.ReadSnapshot(ctx, reviewed.Source.ID, reviewed.SourceSnapshot)
	if err != nil {
		return p, err
	}
	selected := map[string]bool{}
	for _, id := range reviewed.Selected {
		selected[id] = true
	}
	now, err := review(ctx, c, reviewed.Source, reviewed.Target, source, selected, Promotion{ID: reviewed.PromotionID})
	if err != nil {
		return p, err
	}
	if now.View != reviewed.View {
		return p, fmt.Errorf("%s changed since the review, so this promotion would change something else than what was shown; plan again", reviewed.Target.Name)
	}
	return c.Execute(ctx, reviewed.Source.ID, reviewed.PromotionID)
}

// PlanRollback drafts the rollback of an applied promotion and reviews it.
func PlanRollback(ctx context.Context, c *Client, applied Review) (Review, error) {
	p, err := c.Rollback(ctx, applied.Source.ID, applied.PromotionID)
	if err != nil {
		return Review{}, err
	}
	source, err := c.ReadSnapshot(ctx, applied.Source.ID, applied.SourceSnapshot)
	if err != nil {
		return Review{}, err
	}
	// Everything a rollback touches was part of the promotion.
	steps, err := c.Steps(ctx, applied.Source.ID, p.ID)
	if err != nil {
		return Review{}, err
	}
	all := map[string]bool{"": true}
	for _, s := range steps {
		all[s.Source.ID] = true
	}
	r, err := review(ctx, c, applied.Source, applied.Target, source, all, p)
	r.Ticket, r.Rollback, r.SourceSnapshot = applied.Ticket, true, applied.SourceSnapshot
	r.Description = "Undoes promotion " + applied.PromotionID + "."
	// A rollback's operations are all deliberate: no dependency warnings.
	kept := r.Warnings[:0]
	for _, w := range r.Warnings {
		if !strings.Contains(w, "was not selected") {
			kept = append(kept, w)
		}
	}
	r.Warnings = kept
	return r, err
}

// Write saves a review as plan.json, mr-note.md and report.html.
func Write(dir string, r Review) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	plan, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	page, err := HTML(r)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"plan.json": append(plan, '\n'), "mr-note.md": []byte(Markdown(r)), "report.html": page} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Read loads a review written by Write.
func Read(dir string) (Review, error) {
	var r Review
	b, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}
