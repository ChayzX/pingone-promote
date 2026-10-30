package app

import (
	"fmt"
	"strings"
	"testing"
)

func mappingIssues(v PlanView) []string {
	var out []string
	for _, issue := range v.Review.Issues {
		if strings.Contains(issue, "Native mapping for") {
			out = append(out, issue)
		}
	}
	return out
}

// Observed live: after a successful execute, PingOne reports the resolved
// whole-closure resourceMapping (created resources get their real destination
// IDs only at execution). Re-checking it against the pre-execution approval
// flagged a completed promotion as divergent/uncertain.
func TestCompletedPromotionNotFlaggedForResolvedMappings(t *testing.T) {
	h := setup(t)
	v := h.plan()
	if !v.CanDeploy {
		t.Fatalf("valid plan blocked before execute: %+v", v.Review.Issues)
	}
	var deployed PlanView
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &deployed, 200)
	// PingOne now reports resolved IDs that differ from the approved values.
	h.f.mu.Lock()
	h.f.mapping = map[string]string{"f": "resolved-flow", "c": "resolved-connector", "n": "created-template"}
	h.f.mu.Unlock()
	var polled PlanView
	h.call("GET", "/api/plans/"+v.ID, nil, &polled, 200)
	if polled.Status != "COMPLETED" {
		t.Fatalf("want COMPLETED, got %q", polled.Status)
	}
	if issues := mappingIssues(polled); len(issues) > 0 {
		t.Fatalf("completed promotion flagged for PingOne's post-execution mapping: %v", issues)
	}
	// The no-resend guarantee is unchanged: a second deploy is still refused.
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 1 {
		t.Fatalf("execute was replayed: %v", h.f.executed)
	}
}

// Before execution the check is a real safeguard: if PingOne's draft maps an
// approved resource somewhere else, deployment must stay blocked.
func TestPreExecutionMappingDivergenceStillBlocksDeploy(t *testing.T) {
	h := setup(t)
	h.f.mu.Lock()
	h.f.mapping = map[string]string{"f": "somewhere-else", "c": "d"}
	h.f.mu.Unlock()
	v := h.plan()
	if v.CanDeploy {
		t.Fatal("draft whose native mapping diverges from the approval was deployable")
	}
	if issues := mappingIssues(v); len(issues) == 0 {
		t.Fatalf("pre-execution divergence not reported: %v", v.Review.Issues)
	}
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 0 {
		t.Fatal("divergent draft was executed")
	}
}

// A promotion executed outside this app has no recorded intent; PingOne's
// post-execution statuses alone must stop the false divergence report.
func TestExternallyExecutedStatusesNotFlagged(t *testing.T) {
	for _, status := range []string{"IN_PROGRESS", "COMPLETED", "ERROR", "ROLLED_BACK"} {
		t.Run(status, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = status
			h.f.mapping = map[string]string{"f": "resolved-flow", "c": "resolved-connector"}
			h.f.mu.Unlock()
			var polled PlanView
			h.call("GET", "/api/plans/"+v.ID, nil, &polled, 200)
			if issues := mappingIssues(polled); len(issues) > 0 {
				t.Fatalf("%s promotion flagged for post-execution mapping: %v", status, issues)
			}
			if polled.CanDeploy {
				t.Fatalf("%s promotion reported as deployable", status)
			}
		})
	}
}

// A rollback record inherits the forward draft's mappings, but a rollback
// approves no mappings of its own: it restores the whole latest promotion.
// PingOne's resourceMapping on the rollback promotion describes that restore,
// so comparing it with the forward choices could block rollback deploy when it
// is needed most.
func TestRollbackNotBlockedByForwardMappings(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 200)
	// From here PingOne reports mappings that differ from the forward approval,
	// as it does once promotions have executed.
	h.f.mu.Lock()
	h.f.mapping = map[string]string{"f": "resolved-flow", "c": "resolved-connector"}
	h.f.mu.Unlock()
	var rb PlanView
	h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rb, 200)
	if rb.Kind != "rollback" {
		t.Fatalf("want rollback plan, got %+v", rb)
	}
	if issues := mappingIssues(rb); len(issues) > 0 {
		t.Fatalf("rollback review flagged forward mappings: %v", issues)
	}
	if !rb.CanDeploy {
		t.Fatalf("rollback blocked: %v", rb.Review.Issues)
	}
	h.call("POST", "/api/plans/"+rb.ID+"/deploy", map[string]any{"digest": rb.Review.Digest, "confirm": true}, nil, 200)
	if fmt.Sprint(h.f.executed) != "[p1 rb1]" {
		t.Fatalf("wrong executed IDs %v", h.f.executed)
	}
}
