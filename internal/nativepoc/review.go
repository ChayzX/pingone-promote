package nativepoc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pingone-promote/internal/secretscan"
)

// Change is one field of a resource that the promotion sets, adds or
// removes in the target.
type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // set | add | remove
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Secret bool   `json:"secret,omitempty"`
	// Note flags a value a reviewer should look at twice.
	Note string `json:"note,omitempty"`
}

// Item is one resource of the plan, as a reviewer reads it.
type Item struct {
	Action   string `json:"action"` // create | update | delete
	Type     string `json:"type"`
	Name     string `json:"name"`
	SourceID string `json:"sourceId,omitempty"`
	TargetID string `json:"targetId,omitempty"`
	// Selected is false for what PingOne added because the selection
	// depends on it.
	Selected bool     `json:"selected"`
	Changes  []Change `json:"changes,omitempty"`
	Fields   int      `json:"fields"` // fields of the new configuration
	// Flagged is set when a change of this item carries a note.
	Flagged bool `json:"flagged,omitempty"`
}

// Review is the reviewed plan of one promotion. It holds no secret values.
type Review struct {
	Version     int    `json:"version"`
	Ticket      string `json:"ticket"`
	Description string `json:"description,omitempty"`
	Rollback    bool   `json:"rollback,omitempty"`
	Source      Ref    `json:"source"`
	Target      Ref    `json:"target"`
	PromotionID string `json:"promotionId"`
	// Digest identifies the exact operations reviewed. Apply refuses to
	// run a promotion whose operations no longer have this digest.
	Digest string `json:"digest"`
	// View identifies what the reviewer was shown (items and fields).
	View string `json:"view"`
	// SourceSnapshot and Selected let Apply rebuild the same review.
	SourceSnapshot string   `json:"sourceSnapshot"`
	Selected       []string `json:"selected,omitempty"`
	Items          []Item   `json:"items"`
	Warnings       []string `json:"warnings,omitempty"`
}

// Digest is the SHA-256 of a plan's operations in a fixed order.
func Digest(steps []Step) string {
	rows := make([]string, len(steps))
	for i, s := range steps {
		var payload any
		_ = json.Unmarshal(s.Payload, &payload)
		canon, _ := json.Marshal(payload) // map keys sorted
		rows[i] = strings.Join([]string{s.Action, s.URL, s.Source.ID, s.Target.ID, string(canon)}, "\x00")
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\x01")))
	return hex.EncodeToString(sum[:])
}

// BuildReview compares each operation's new configuration with what the
// target has now.
func BuildReview(source, target *Snapshot, sourceEnv, targetEnv Ref, selected map[string]bool, p Promotion, steps []Step) Review {
	r := Review{Version: 1, Source: sourceEnv, Target: targetEnv, PromotionID: p.ID, Digest: Digest(steps), SourceSnapshot: source.ID}
	for _, s := range steps {
		it := Item{Action: strings.ToLower(s.Action), Type: TypeOf(s.URL), SourceID: s.Source.ID, TargetID: s.Target.ID, Selected: selected[s.Source.ID]}
		var after map[string]any
		_ = json.Unmarshal(s.Payload, &after)
		before := map[string]any(nil)
		if t, ok := target.Resources[s.Target.ID]; ok && s.Target.ID != "" {
			before, it.Name = t.Configuration, t.Name
		}
		if src, ok := source.Resources[s.Source.ID]; ok && src.Name != "" {
			it.Name = src.Name
		}
		if it.Name == "" {
			it.Name = s.Source.Name + s.Target.Name
		}
		if it.Type == "" {
			it.Type = "resource"
		}
		a, b := flatten(after), flatten(before)
		it.Fields = len(a)
		switch it.Action {
		case "create":
			// Every field is new; the report shows a short summary instead.
		case "delete":
			it.Fields = len(b)
		default:
			it.Changes = diff(b, a, sourceEnv)
			if len(it.Changes) == 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s “%s” is in the plan as an update but nothing differs from %s; PingOne may rewrite it unchanged", it.Type, it.Name, targetEnv.Name))
			}
		}
		for _, c := range it.Changes {
			if c.Note != "" {
				it.Flagged = true
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s “%s”, %s: %s", it.Type, it.Name, c.Path, c.Note))
			}
		}
		if !it.Selected && it.Action != "create" {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s “%s” was not selected: PingOne added it as a dependency and will %s it in %s", it.Type, it.Name, it.Action, targetEnv.Name))
		}
		r.Items = append(r.Items, it)
	}
	sort.SliceStable(r.Items, func(i, j int) bool {
		a, b := r.Items[i], r.Items[j]
		if a.Selected != b.Selected {
			return a.Selected
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Name < b.Name
	})
	sort.Strings(r.Warnings)
	return r
}

type leaf struct {
	text   string
	secret bool
}

// flatten turns a configuration into path -> value. A list of objects is
// keyed by each object's id, name or key, so a node added in the middle of
// a flow is one addition, not a change to every node after it.
func flatten(v map[string]any) map[string]leaf {
	out := map[string]leaf{}
	var walk func(path, key string, x any)
	walk = func(path, key string, x any) {
		switch t := x.(type) {
		case map[string]any:
			if len(t) == 0 && path != "" {
				out[path] = leaf{text: "{}"}
			}
			for k, e := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, k, e)
			}
		case []any:
			if len(t) == 0 {
				out[path] = leaf{text: "[]"}
			}
			seen := map[string]bool{}
			for i, e := range t {
				label := fmt.Sprintf("[%d]", i)
				if m, ok := e.(map[string]any); ok {
					for _, idKey := range []string{"id", "name", "key"} {
						if s, ok := m[idKey].(string); ok && s != "" && !seen[s] {
							label, seen[s] = "["+s+"]", true
							break
						}
					}
				}
				walk(path+label, key, e)
			}
		default:
			b, _ := json.Marshal(t)
			text := string(b)
			if s, ok := t.(string); ok {
				text = s
			}
			out[path] = leaf{text: text, secret: isSecret(key, t)}
		}
	}
	walk("", "", map[string]any(v))
	return out
}

// isSecret reports whether a value must not be shown: its key names a
// credential or the value itself is one.
func isSecret(key string, v any) bool {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" || secretscan.TemplateOnly.MatchString(s) {
		return false
	}
	return secretscan.CredentialKey(key) || secretscan.CredentialValue(s)
}

func show(l leaf) string {
	if l.secret {
		return "(secret, not shown)"
	}
	if len(l.text) > 160 {
		return l.text[:157] + "..."
	}
	return l.text
}

func diff(before, after map[string]leaf, sourceEnv Ref) []Change {
	var out []Change
	for p, a := range after {
		b, had := before[p]
		switch {
		case !had:
			out = append(out, Change{Path: p, Kind: "add", After: show(a), Secret: a.secret, Note: envNote(a, sourceEnv)})
		case a.text != b.text:
			c := Change{Path: p, Kind: "set", Before: show(b), After: show(a), Secret: a.secret || b.secret, Note: envNote(a, sourceEnv)}
			if a.secret && b.secret {
				c.After = "(a different secret, not shown)"
			}
			out = append(out, c)
		}
	}
	for p, b := range before {
		if _, kept := after[p]; !kept {
			out = append(out, Change{Path: p, Kind: "remove", Before: show(b), Secret: b.secret})
		}
	}
	out = group(out, "add", before, after)
	out = group(out, "remove", after, before)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// group folds the fields of a list element that is wholly new (or wholly
// removed) into one line: a node added to a flow reads as one addition.
// other is the side that must have nothing under the element; own is the
// side that has it.
func group(changes []Change, kind string, other, own map[string]leaf) []Change {
	element := func(path string) string {
		for i := 0; i < len(path); i++ {
			if path[i] != ']' {
				continue
			}
			prefix := path[:i+1]
			taken := false
			for p := range other {
				if strings.HasPrefix(p, prefix) {
					taken = true
					break
				}
			}
			if !taken {
				return prefix
			}
		}
		return ""
	}
	count := map[string]int{}
	var out []Change
	for _, c := range changes {
		if c.Kind != kind {
			out = append(out, c)
			continue
		}
		e := element(c.Path)
		if e == "" {
			out = append(out, c)
			continue
		}
		count[e]++
	}
	for e, n := range count {
		label := fmt.Sprintf("%d fields", n)
		if l, ok := own[e+".name"]; ok && !l.secret {
			label = "“" + l.text + "”, " + label
		}
		c := Change{Path: e, Kind: kind}
		if kind == "add" {
			c.After = "new: " + label
		} else {
			c.Before = "was: " + label
		}
		out = append(out, c)
	}
	return out
}

// envNote flags a new value that names the source environment (its ID, or
// its name as a word in a host or path): the target would point at the
// source. Such a value usually needs a promotion variable.
func envNote(l leaf, sourceEnv Ref) string {
	if l.secret {
		return ""
	}
	t := strings.ToLower(l.text)
	if sourceEnv.ID != "" && strings.Contains(t, strings.ToLower(sourceEnv.ID)) {
		return "the new value holds the ID of " + sourceEnv.Name
	}
	n := strings.ToLower(sourceEnv.Name)
	if n == "" || !strings.Contains(t, "://") {
		return ""
	}
	for _, f := range strings.FieldsFunc(t, func(r rune) bool {
		return r == '.' || r == '/' || r == '-' || r == ':' || r == '_' || r == '?' || r == '=' || r == '&'
	}) {
		if f == n {
			return "the new value looks specific to " + sourceEnv.Name + "; it may need a promotion variable"
		}
	}
	return ""
}

// Counts is how many items a review creates, updates and deletes.
func (r Review) Counts() (create, update, del int) {
	for _, it := range r.Items {
		switch it.Action {
		case "create":
			create++
		case "delete":
			del++
		default:
			update++
		}
	}
	return
}
