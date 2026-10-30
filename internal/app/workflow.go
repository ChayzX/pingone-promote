package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
	"pingone-promote/internal/store"
)

type inventoryPair struct {
	SourceID string `json:"sourceInventoryId"`
	TargetID string `json:"targetInventoryId"`
}
type PlanView struct {
	ID          string           `json:"id"`
	NativeID    string           `json:"nativeId"`
	Kind        string           `json:"kind"`
	Description string           `json:"description"`
	Status      string           `json:"status"`
	Errors      any              `json:"errors,omitempty"`
	Review      promotion.Review `json:"review"`
	CanDeploy   bool             `json:"canDeploy"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func (s *Server) pair(p inventoryPair) (*inventorySession, *inventorySession, error) {
	a, b := s.inventories[p.SourceID], s.inventories[p.TargetID]
	if a == nil || b == nil {
		return nil, nil, errors.New("load both source and destination resources first")
	}
	if a.EnvironmentID == b.EnvironmentID {
		return nil, nil, errors.New("choose different source and destination environments")
	}
	if a.OrganizationID != b.OrganizationID {
		return nil, nil, errors.New("both environments must belong to the same PingOne tenant")
	}
	return a, b, nil
}
func suggestMappings(a, b pingone.Inventory, native map[string]string) map[string]string {
	dst := map[string]pingone.Resource{}
	for _, r := range b.Resources {
		dst[r.ID] = r
	}
	out := map[string]string{}
	used := map[string]bool{}
	for _, r := range a.Resources {
		if d, ok := dst[native[r.ID]]; ok && d.Type == r.Type && !used[d.ID] {
			out[r.ID] = d.ID
			used[d.ID] = true
		}
	}
	for _, r := range a.Resources {
		if out[r.ID] != "" {
			continue
		}
		var matches []string
		for _, d := range b.Resources {
			if r.Type == d.Type && strings.EqualFold(strings.TrimSpace(r.Name), strings.TrimSpace(d.Name)) {
				matches = append(matches, d.ID)
			}
		}
		if len(matches) == 1 && !used[matches[0]] {
			out[r.ID] = matches[0]
			used[matches[0]] = true
		}
	}
	return out
}
func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		inventoryPair
		Selected    []string          `json:"selected"`
		Mappings    map[string]string `json:"mappings"`
		Description string            `json:"description"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	source, target, err := s.pair(input.inventoryPair)
	if err != nil {
		return err
	}
	body, err := promotion.BuildRequest(source.Inventory, target.Inventory, input.Selected, input.Mappings, input.Description)
	if err != nil {
		return err
	}
	sp, err := s.profile(source.ProfileID)
	if err != nil {
		return err
	}
	tp, err := s.profile(target.ProfileID)
	if err != nil {
		return err
	}
	c, err := s.client(sp)
	if err != nil {
		return err
	}
	record := store.Record{ID: RandomToken(), Kind: "promotion", Description: input.Description, Source: sp, Target: tp, Selected: input.Selected, Mappings: input.Mappings, SourceSnapshotID: source.SnapshotID, TargetSnapshotID: target.SnapshotID, SourceOrganizationID: source.OrganizationID, TargetOrganizationID: target.OrganizationID, SourceAPIOrigin: source.APIOrigin, TargetAPIOrigin: target.APIOrigin, CreatedAt: now(), Status: "CREATING", Intent: "create"}
	for _, inv := range []pingone.Inventory{source.Inventory, target.Inventory} {
		for _, res := range inv.Resources {
			record.Resources = append(record.Resources, store.ResourceRef{ID: res.ID, URL: res.URL, Name: res.Name, Type: res.Type})
		}
	}
	if err = s.store.SaveRecord(record); err != nil {
		return err
	}
	var native pingone.Promotion
	if err = c.Do(r.Context(), "POST", pingone.EnvPath(source.EnvironmentID)+"/promotions", "", body, &native); err != nil {
		record.Status = "DRAFT_CREATION_UNCERTAIN"
		record.Intent = "create_uncertain"
		record.UpdatedAt = now()
		_ = s.store.SaveRecord(record)
		return problem(502, err)
	}
	if native.ID == "" {
		return problem(502, errors.New("PingOne omitted the created promotion identity; nothing has been deployed"))
	}
	record.NativeID = native.ID
	record.Status = native.Status
	record.Intent = ""
	record.UpdatedAt = now()
	if err = s.store.SaveRecord(record); err != nil {
		return err
	}
	ps := &planSession{Record: record, Source: source.Inventory, Target: target.Inventory}
	s.plans[record.ID] = ps
	view, err := s.refresh(r, ps)
	if err != nil {
		return err
	}
	writeJSON(w, view)
	return nil
}
func (s *Server) loadPlan(id string) (*planSession, error) {
	if p := s.plans[id]; p != nil {
		return p, nil
	}
	record, err := s.store.GetRecord(id)
	if err != nil {
		return nil, problem(404, err)
	}
	a := pingone.Inventory{APIOrigin: record.SourceAPIOrigin, EnvironmentID: record.Source.EnvironmentID, OrganizationID: record.SourceOrganizationID, SnapshotID: record.SourceSnapshotID, Resources: []pingone.Resource{}}
	b := pingone.Inventory{APIOrigin: record.TargetAPIOrigin, EnvironmentID: record.Target.EnvironmentID, OrganizationID: record.TargetOrganizationID, SnapshotID: record.TargetSnapshotID, Resources: []pingone.Resource{}}
	for _, ref := range record.Resources {
		res := pingone.Resource{ID: ref.ID, URL: ref.URL, Type: ref.Type, Name: ref.Name}
		u, err := url.Parse(ref.URL)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(u.Path, pingone.EnvPath(a.EnvironmentID)+"/") {
			a.Resources = append(a.Resources, res)
		} else if strings.HasPrefix(u.Path, pingone.EnvPath(b.EnvironmentID)+"/") {
			b.Resources = append(b.Resources, res)
		} else {
			return nil, errors.New("history resource does not belong to the recorded environments")
		}
	}
	p := &planSession{Record: record, Source: a, Target: b}
	s.plans[id] = p
	return p, nil
}
func (s *Server) refresh(r *http.Request, ps *planSession) (PlanView, error) {
	rec := &ps.Record
	v := PlanView{ID: rec.ID, NativeID: rec.NativeID, Kind: rec.Kind, Description: rec.Description, Status: rec.Status, Review: promotion.Review{Issues: []string{}, Steps: []promotion.StepReview{}}}
	if rec.NativeID == "" {
		v.Review.Issues = append(v.Review.Issues, "This draft has no known PingOne ID. No deployment was requested; load resources and create a new draft.")
		return v, nil
	}
	c, err := s.client(rec.Source)
	if err != nil {
		return v, err
	}
	if _, err = s.client(rec.Target); err != nil {
		return v, err
	}
	native, err := c.GetPromotion(r.Context(), rec.Source.EnvironmentID, rec.NativeID)
	if err != nil {
		return v, problem(502, err)
	}
	v.Status = native.Status
	if len(native.Errors) > 0 {
		var raw any
		if jsonError := json.Unmarshal(native.Errors, &raw); jsonError == nil {
			switch value := raw.(type) {
			case nil:
			case []any:
				if len(value) > 0 {
					v.Errors = promotion.Redact(value)
				}
			case map[string]any:
				if len(value) > 0 {
					v.Errors = promotion.Redact(value)
				}
			default:
				v.Errors = promotion.Redact(raw)
			}
		}
	}
	// A plan is polled every ~2s while it runs. Persisting on every poll
	// rewrites the whole records file (with fsync) under s.mu even when nothing
	// changed; at tens of records that lock is busy a large fraction of the
	// time. Save only when the native status actually moved. Intent changes
	// (deploy, rollback) are persisted explicitly at those call sites, so the
	// only field that would change on an unchanged poll is UpdatedAt, which is
	// cosmetic.
	if native.Status != rec.Status {
		rec.Status = native.Status
		rec.UpdatedAt = now()
		if err = s.store.SaveRecord(*rec); err != nil {
			return v, err
		}
	}
	// Reconcile the original record after a completed native rollback so history
	// cannot continue to offer another rollback of the restored promotion.
	if rec.Kind == "rollback" && native.Status == "COMPLETED" && rec.ParentID != "" {
		original, readErr := s.store.GetRecord(rec.ParentID)
		if readErr == nil {
			state, stateErr := c.GetPromotion(r.Context(), original.Source.EnvironmentID, original.NativeID)
			if stateErr == nil {
				original.Status = state.Status
				original.UpdatedAt = now()
				if err = s.store.SaveRecord(original); err != nil {
					return v, err
				}
				if cached := s.plans[original.ID]; cached != nil {
					cached.Record = original
				}
			}
		}
	}
	pending := native.Status == "NEW" || native.Status == "PREPARING" || native.Status == "ADDITIONAL_SNAPSHOTS_REQUESTED" || native.Status == "ROLLBACK_NEW" || native.Status == "ROLLBACK_PREPARING"
	if pending {
		v.Review.Issues = append(v.Review.Issues, "PingOne is preparing the plan. Refresh to load the complete operation preview.")
		return v, nil
	}
	steps, err := c.Steps(r.Context(), rec.Source.EnvironmentID, rec.NativeID)
	if err != nil {
		return v, problem(502, err)
	}
	effective := map[string]string{}
	for k, val := range native.Mapping {
		effective[k] = val
	}
	for k, val := range rec.Mappings {
		effective[k] = val
	}
	v.Review = promotion.Inspect(ps.Source, ps.Target, rec.Selected, effective, steps, rec.Kind == "rollback")
	if native.Source.ID != rec.Source.EnvironmentID || native.Target.ID != rec.Target.EnvironmentID {
		v.Review.Issues = append(v.Review.Issues, "Native promotion source/destination differs from the approved environment pair.")
	}
	for k, val := range rec.Mappings {
		if actual := native.Mapping[k]; actual != "" && actual != val {
			v.Review.Issues = append(v.Review.Issues, fmt.Sprintf("Native mapping for %s differs from your selected destination.", k))
		}
	}
	if native.SourceSnapshot.ID != "" && rec.Kind != "rollback" && native.SourceSnapshot.ID != rec.SourceSnapshotID {
		v.Review.Issues = append(v.Review.Issues, "Native source snapshot differs from the pinned inventory.")
	}
	if rec.Intent != "" && native.Status == "READY" {
		v.Review.Issues = append(v.Review.Issues, "An execution or rollback request was already sent or its outcome is uncertain. Refresh status; this app will not automatically resend it.")
	}
	v.CanDeploy = native.Status == "READY" && len(v.Review.Issues) == 0 && v.Errors == nil && rec.Intent == ""
	return v, nil
}
func (s *Server) deploy(w http.ResponseWriter, r *http.Request, ps *planSession) error {
	var input struct {
		Digest  string `json:"digest"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if !input.Confirm || input.Digest == "" {
		return problem(409, errors.New("review the complete plan and confirm deployment first"))
	}
	fresh, err := s.refresh(r, ps)
	if err != nil {
		return err
	}
	if !fresh.CanDeploy {
		return problem(409, errors.New("deployment blocked: native status, selection checks, or previous execution prevents it; refresh the plan"))
	}
	if !equal(fresh.Review.Digest, input.Digest) {
		return problem(409, errors.New("the native plan changed after review; refresh and review the updated operations"))
	}
	ps.Record.Intent = "execute_requested"
	ps.Record.UpdatedAt = now()
	if err = s.store.SaveRecord(ps.Record); err != nil {
		return err
	}
	c, err := s.client(ps.Record.Source)
	if err != nil {
		return err
	}
	var native pingone.Promotion
	if err = c.Do(r.Context(), "POST", pingone.EnvPath(ps.Record.Source.EnvironmentID)+"/promotions/"+ps.Record.NativeID, pingone.ExecuteType, nil, &native); err != nil {
		return problem(502, err)
	}
	if native.ID != "" && native.ID != ps.Record.NativeID {
		return problem(502, errors.New("execute returned an unexpected promotion identity; refresh native status"))
	}
	ps.Record.Intent = "execute_sent"
	if native.Status != "" {
		ps.Record.Status = native.Status
	}
	ps.Record.UpdatedAt = now()
	if err = s.store.SaveRecord(ps.Record); err != nil {
		return err
	}
	view, err := s.refresh(r, ps)
	if err != nil {
		return err
	}
	writeJSON(w, view)
	return nil
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request, ps *planSession) error {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if !input.Confirm {
		return problem(409, errors.New("confirm that rollback restores the entire latest promotion and may overwrite later changes"))
	}
	view, err := s.refresh(r, ps)
	if err != nil {
		return err
	}
	if ps.Record.Kind == "rollback" || (view.Status != "COMPLETED" && view.Status != "ERROR") || strings.HasPrefix(ps.Record.Intent, "rollback") {
		return problem(409, errors.New("only a completed or failed forward promotion can request rollback; PingOne enforces latest-promotion eligibility"))
	}
	ps.Record.Intent = "rollback_requested"
	ps.Record.UpdatedAt = now()
	if err = s.store.SaveRecord(ps.Record); err != nil {
		return err
	}
	c, err := s.client(ps.Record.Source)
	if err != nil {
		return err
	}
	var native pingone.Promotion
	if err = c.Do(r.Context(), "POST", pingone.EnvPath(ps.Record.Source.EnvironmentID)+"/promotions/"+ps.Record.NativeID, pingone.RollbackType, nil, &native); err != nil {
		return problem(502, err)
	}
	if native.ID == "" || native.ID == ps.Record.NativeID || native.Previous.ID != ps.Record.NativeID {
		return problem(502, errors.New("rollback did not return a distinct plan linked to the original; inspect native status before another request"))
	}
	record := ps.Record
	record.ID = RandomToken()
	record.NativeID = native.ID
	record.Kind = "rollback"
	record.ParentID = ps.Record.ID
	record.Status = native.Status
	record.Intent = ""
	record.CreatedAt = now()
	record.UpdatedAt = now()
	record.Description = "Rollback: " + ps.Record.Description
	if err = s.store.SaveRecord(record); err != nil {
		return err
	}
	rb := &planSession{Record: record, Source: ps.Source, Target: ps.Target}
	s.plans[record.ID] = rb
	result, err := s.refresh(r, rb)
	if err != nil {
		return err
	}
	writeJSON(w, result)
	return nil
}
