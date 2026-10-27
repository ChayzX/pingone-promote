// Package gitops implements portable, secret-redacted requests and reviewed native plans.
package gitops

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"time"

	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

const maxRequestBytes = 8 << 20

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
var hexID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hashID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Environment struct {
	ID             string `json:"id"`
	Region         string `json:"region"`
	OrganizationID string `json:"organizationId"`
	SnapshotID     string `json:"snapshotId"`
}
type Request struct {
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	CreatedAt      string            `json:"createdAt"`
	Description    string            `json:"description"`
	Source         Environment       `json:"source"`
	Target         Environment       `json:"target"`
	Selected       []string          `json:"selected"`
	Mappings       map[string]string `json:"mappings"`
	Configurations map[string]any    `json:"configurations"`
	Fingerprints   map[string]string `json:"fingerprints"`
}

func NewRequest(source, target pingone.Inventory, sourceRegion, targetRegion string, selected []string, mappings map[string]string, description string) (Request, error) {
	r := Request{}
	if _, err := promotion.BuildRequest(source, target, selected, mappings, description); err != nil {
		return r, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return r, errors.New("could not create request identity")
	}
	r = Request{Version: 1, ID: hex.EncodeToString(id), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Description: description, Source: Environment{source.EnvironmentID, sourceRegion, source.OrganizationID, source.SnapshotID}, Target: Environment{target.EnvironmentID, targetRegion, target.OrganizationID, target.SnapshotID}, Selected: append([]string(nil), selected...), Mappings: map[string]string{}, Configurations: map[string]any{}, Fingerprints: map[string]string{}}
	sort.Strings(r.Selected)
	for k, v := range mappings {
		r.Mappings[k] = v
	}
	for _, id := range selected {
		for _, res := range source.Resources {
			if res.ID == id {
				if len(res.Configuration) == 0 {
					return Request{}, errors.New("selected resource has no available configuration")
				}
				redacted := promotion.Redact(res.Configuration)
				r.Configurations[id] = redacted
				r.Fingerprints[id] = fingerprint(redacted)
			}
		}
	}
	return r, Validate(r)
}
func fingerprint(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func Validate(r Request) error {
	if r.Version != 1 || !hexID.MatchString(r.ID) {
		return errors.New("unsupported request version or invalid request identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err != nil {
		return errors.New("invalid request creation time")
	}
	if len(r.Description) > 4000 {
		return errors.New("request description is too long")
	}
	for _, env := range []Environment{r.Source, r.Target} {
		if !safeID.MatchString(env.ID) || !safeID.MatchString(env.OrganizationID) || !safeID.MatchString(env.SnapshotID) || pingone.RegionDomains[env.Region] == "" {
			return errors.New("request requires safe environment, region, organization and pinned snapshot identities")
		}
	}
	if r.Source.ID == r.Target.ID || r.Source.OrganizationID != r.Target.OrganizationID {
		return errors.New("source and target must be distinct environments in the same organization")
	}
	if len(r.Selected) == 0 || len(r.Selected) > 10000 || len(r.Configurations) != len(r.Selected) || len(r.Fingerprints) != len(r.Selected) {
		return errors.New("request selection and configuration/fingerprint sets must match exactly")
	}
	seen := map[string]bool{}
	for _, id := range r.Selected {
		if !safeID.MatchString(id) || seen[id] {
			return errors.New("invalid or duplicate selected resource identity")
		}
		seen[id] = true
		config, ok := r.Configurations[id]
		obj, isObject := config.(map[string]any)
		if !ok || !isObject || len(obj) == 0 || !hashID.MatchString(r.Fingerprints[id]) || fingerprint(config) != r.Fingerprints[id] {
			return fmt.Errorf("selected resource %q has missing or mismatched configuration fingerprint", id)
		}
		if !reflect.DeepEqual(config, promotion.Redact(config)) {
			return errors.New("configuration contains unredacted sensitive fields")
		}
	}
	targets := map[string]bool{}
	for k, v := range r.Mappings {
		if !safeID.MatchString(k) || !safeID.MatchString(v) || targets[v] {
			return errors.New("invalid or conflicting resource mapping")
		}
		targets[v] = true
	}
	return nil
}
func Decode(reader io.Reader) (Request, error) {
	var r Request
	data, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil {
		return r, errors.New("could not read request")
	}
	if len(data) > maxRequestBytes {
		return r, errors.New("request exceeds 8 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return r, errors.New("invalid request JSON or unknown fields")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return r, errors.New("request must contain exactly one JSON document")
	}
	return r, Validate(r)
}
func Encode(r Request) ([]byte, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if len(b) > maxRequestBytes {
		return nil, errors.New("request exceeds 8 MiB")
	}
	return b, err
}
