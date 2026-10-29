package app

import (
	"net/http"
	"pingone-promote/internal/pingone"
	"testing"
)

func TestLocalExecutionDeniedUnlessTargetExplicitlyAllowlisted(t *testing.T) {
	h := setup(t)
	s := h.server.Config.Handler.(*Server)
	s.localTargets = map[string]bool{}
	var a, b struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.source.ID}, &a, 200)
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.target.ID}, &b, 200)
	h.call("POST", "/api/plans", map[string]any{"sourceInventoryId": a.ID, "targetInventoryId": b.ID, "selected": []string{"f"}, "mappings": map[string]string{"f": "g"}}, nil, http.StatusForbidden)
	if h.f.created != nil {
		t.Fatal("GitLab-only target created a local native plan")
	}
}
func TestRemovingAllowlistBlocksOldPlanDeployAndRollback(t *testing.T) {
	h := setup(t)
	v := h.plan()
	s := h.server.Config.Handler.(*Server)
	s.localTargets = map[string]bool{}
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusForbidden)
	h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]any{"confirm": true}, nil, http.StatusForbidden)
	if len(h.f.executed) > 0 {
		t.Fatal("old plan bypassed GitLab policy")
	}
}
func TestTargetAliasCannotBypassProcessPolicy(t *testing.T) {
	h := setup(t)
	s := h.server.Config.Handler.(*Server)
	s.localTargets = map[string]bool{}
	p := h.target
	p.ID = ""
	p.Name = "Definitely development"
	alias, err := s.store.SaveProfile(p, "")
	if err != nil {
		t.Fatal(err)
	}
	s.localTargets = map[string]bool{"some-other-environment": true}
	if s.localAllowed(pingone.Profile{ID: alias.ID, Name: alias.Name, EnvironmentID: alias.EnvironmentID}) {
		t.Fatal("alias renamed production into local route")
	}
}
