package gitops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"pingone-promote/internal/pingone"
)

// An Intent is the small, human-reviewed request that the DaVinci front door
// commits as intents/<id>/intent.json. It holds no configuration: the runner
// turns it into a full Request (redacted configurations and fingerprints)
// inside the protected pipeline, so redaction and fingerprinting stay in Go.
type Intent struct {
	Version     int               `json:"version"`
	ID          string            `json:"id"`
	RequestedBy Requester         `json:"requestedBy"`
	Source      IntentEnvironment `json:"source"`
	Target      IntentEnvironment `json:"target"`
	Selected    []string          `json:"selected"`
	Mappings    map[string]string `json:"mappings"`
	Description string            `json:"description"`
}

// Requester is the PingOne identity that signed in to the front door.
type Requester struct {
	PingOneUserID string `json:"pingoneUserId"`
	Username      string `json:"username"`
}

// IntentEnvironment names an environment without pinning a snapshot; the
// runner takes fresh snapshots when it builds the request.
type IntentEnvironment struct {
	ID     string `json:"id"`
	Region string `json:"region"`
}

const maxIntentBytes = 1 << 20

var intentKeys = map[string][]string{
	"":            {"version", "id", "requestedBy", "source", "target", "selected", "mappings", "description"},
	"requestedBy": {"pingoneUserId", "username"},
	"source":      {"id", "region"},
	"target":      {"id", "region"},
}

// DecodeIntent strictly parses one intent document. Besides unknown fields it
// rejects duplicate keys and keys that differ from the schema only by case,
// which encoding/json would otherwise accept silently (last or
// case-insensitive match wins), so the reviewed text is what takes effect.
func DecodeIntent(reader io.Reader) (Intent, error) {
	var in Intent
	data, err := io.ReadAll(io.LimitReader(reader, maxIntentBytes+1))
	if err != nil {
		return in, errors.New("could not read intent")
	}
	if len(data) > maxIntentBytes {
		return in, errors.New("intent exceeds 1 MiB")
	}
	if err = checkJSONKeys(data); err != nil {
		return in, err
	}
	if err = checkExactKeys(data); err != nil {
		return in, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return in, errors.New("invalid intent JSON or unknown fields")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return in, errors.New("intent must contain exactly one JSON document")
	}
	return in, ValidateIntent(in)
}

// ValidateIntent checks an intent's identities and limits.
func ValidateIntent(in Intent) error {
	if in.Version != 1 || !hexID.MatchString(in.ID) {
		return errors.New("unsupported intent version or invalid intent identity")
	}
	if !safeID.MatchString(in.RequestedBy.PingOneUserID) || !printable(in.RequestedBy.Username, 256) || in.RequestedBy.Username == "" {
		return errors.New("intent requires the requesting PingOne user ID and username")
	}
	for _, env := range []IntentEnvironment{in.Source, in.Target} {
		if !safeID.MatchString(env.ID) || pingone.RegionDomains[env.Region] == "" {
			return errors.New("intent requires safe source and target environment identities and regions")
		}
	}
	if in.Source.ID == in.Target.ID {
		return errors.New("source and target must be distinct environments")
	}
	if len(in.Selected) == 0 || len(in.Selected) > 10000 {
		return errors.New("intent must select between 1 and 10000 resources")
	}
	seen := map[string]bool{}
	for _, id := range in.Selected {
		if !safeID.MatchString(id) || seen[id] {
			return errors.New("invalid or duplicate selected resource identity")
		}
		seen[id] = true
	}
	targets := map[string]bool{}
	for k, v := range in.Mappings {
		if !safeID.MatchString(k) || !safeID.MatchString(v) || targets[v] {
			return errors.New("invalid or conflicting resource mapping")
		}
		targets[v] = true
	}
	if !printable(in.Description, 3000) {
		return errors.New("intent description is too long or contains control characters")
	}
	return nil
}

// RequestFromIntent builds the full request from fresh source and target
// inventories. The request keeps the intent's ID, so its identity stays bound
// to the reviewed intents/<id>/ path, and records who asked in the description.
func RequestFromIntent(in Intent, source, target pingone.Inventory) (Request, error) {
	if err := ValidateIntent(in); err != nil {
		return Request{}, err
	}
	if source.EnvironmentID != in.Source.ID || target.EnvironmentID != in.Target.ID {
		return Request{}, errors.New("inventories do not match the intent's environments")
	}
	desc := fmt.Sprintf("Requested by %s (PingOne user %s) via the DaVinci front door, intent %s.", in.RequestedBy.Username, in.RequestedBy.PingOneUserID, in.ID)
	if in.Description != "" {
		desc += "\n\n" + in.Description
	}
	r, err := NewRequest(source, target, in.Source.Region, in.Target.Region, in.Selected, in.Mappings, desc)
	if err != nil {
		return Request{}, err
	}
	r.ID = in.ID
	return r, Validate(r)
}

func printable(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

// checkJSONKeys walks the token stream and rejects an object that repeats a
// key, or holds two keys equal under case folding.
func checkJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var stack []*keyFrame
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("invalid intent JSON")
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			if key, ok := tok.(string); ok {
				top := stack[n-1]
				folded := strings.ToLower(key)
				if top.keys[folded] {
					return fmt.Errorf("intent repeats the key %q (or a case variant of it)", key)
				}
				top.keys[folded] = true
				top.wantKey = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &keyFrame{object: true, keys: map[string]bool{}, wantKey: true})
		case json.Delim('['):
			stack = append(stack, &keyFrame{})
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone(stack)
		default:
			valueDone(stack)
		}
	}
}

type keyFrame struct {
	object  bool
	keys    map[string]bool
	wantKey bool
}

// valueDone records that a complete value ended inside the enclosing object,
// so the next string token there is a key again.
func valueDone(stack []*keyFrame) {
	if n := len(stack); n > 0 && stack[n-1].object {
		stack[n-1].wantKey = true
	}
}

// checkExactKeys requires schema keys to match exactly, case included.
func checkExactKeys(data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return errors.New("intent must be a JSON object")
	}
	if err := exactKeys(top, intentKeys[""]); err != nil {
		return err
	}
	for _, name := range []string{"requestedBy", "source", "target"} {
		var obj map[string]json.RawMessage
		if raw, ok := top[name]; ok {
			if err := json.Unmarshal(raw, &obj); err != nil {
				return fmt.Errorf("intent %s must be an object", name)
			}
			if err := exactKeys(obj, intentKeys[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactKeys(obj map[string]json.RawMessage, allowed []string) error {
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range obj {
		if !ok[k] {
			return fmt.Errorf("intent has an unknown or wrongly cased key %q", k)
		}
	}
	return nil
}
