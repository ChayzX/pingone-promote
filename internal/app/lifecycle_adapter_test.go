package app

import "testing"

func TestLifecycleUnknownStatusKeepsMappingChecks(t *testing.T) {
	for _, status := range []string{"completed", "Error", "in_progress", "rolled_back", "", "FUTURE", "VALIDATION_FAILED"} {
		t.Run(status, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = status
			h.f.mapping = map[string]string{"f": "elsewhere"}
			h.f.mu.Unlock()
			var got PlanView
			h.call("GET", "/api/plans/"+v.ID, nil, &got, 200)
			if len(mappingIssues(got)) == 0 || got.CanDeploy {
				t.Fatalf("unknown status relaxed review: %+v", got)
			}
			h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
			if len(h.f.executed) != 0 {
				t.Fatal("unknown dispatched")
			}
		})
	}
}
func TestLifecyclePreparationSkipsSteps(t *testing.T) {
	for _, status := range []string{"NEW", "PREPARING", "ADDITIONAL_SNAPSHOTS_REQUESTED", "ROLLBACK_NEW", "ROLLBACK_PREPARING"} {
		t.Run(status, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = status
			h.f.afterSteps = func() { t.Error("preparation fetched steps") }
			h.f.mu.Unlock()
			var got PlanView
			h.call("GET", "/api/plans/"+v.ID, nil, &got, 200)
			if got.CanDeploy || len(got.Review.Issues) == 0 {
				t.Fatalf("preparation deployable: %+v", got)
			}
		})
	}
}
func TestLifecycleReadyIntentCannotReplay(t *testing.T) {
	for _, intent := range []string{"execute_requested", "rollback_requested", "create_uncertain"} {
		t.Run(intent, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			ps := h.app.plans[v.ID]
			ps.Record.Intent = intent
			if err := h.app.store.SaveRecord(ps.Record); err != nil {
				t.Fatal(err)
			}
			var got PlanView
			h.call("GET", "/api/plans/"+v.ID, nil, &got, 200)
			if got.CanDeploy {
				t.Fatal("READY with intent deployable")
			}
			h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
			if len(h.f.executed) != 0 {
				t.Fatal("intent replayed")
			}
		})
	}
}
func TestLifecycleDurableEvidenceKeepsResolvedMappings(t *testing.T) {
	for _, intent := range []string{"execute_requested", "rollback_requested"} {
		t.Run(intent, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			ps := h.app.plans[v.ID]
			ps.Record.Intent = intent
			if err := h.app.store.SaveRecord(ps.Record); err != nil {
				t.Fatal(err)
			}
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = "future"
			h.f.mapping = map[string]string{"f": "resolved"}
			h.f.mu.Unlock()
			var got PlanView
			h.call("GET", "/api/plans/"+v.ID, nil, &got, 200)
			if len(mappingIssues(got)) != 0 || got.CanDeploy {
				t.Fatalf("durable evidence lost: %+v", got)
			}
		})
	}
}

func TestLifecycleRollbackEligibility(t *testing.T) {
	for _, tc := range []struct {
		status, kind, intent string
		allowed              bool
	}{
		{"COMPLETED", "promotion", "", true}, {"ERROR", "promotion", "", true}, {"ROLLED_BACK", "promotion", "", false}, {"completed", "promotion", "", false}, {"FUTURE", "promotion", "", false}, {"READY", "promotion", "", false}, {"COMPLETED", "rollback", "", false}, {"COMPLETED", "promotion", "rollback_requested", false},
	} {
		t.Run(tc.status+tc.kind+tc.intent, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			ps := h.app.plans[v.ID]
			ps.Record.Kind = tc.kind
			ps.Record.Intent = tc.intent
			if err := h.app.store.SaveRecord(ps.Record); err != nil {
				t.Fatal(err)
			}
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = tc.status
			h.f.mu.Unlock()
			code := 409
			if tc.allowed {
				code = 200
			}
			h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, nil, code)
			want := 0
			if tc.allowed {
				want = 1
			}
			if h.f.rollbackRequests != want {
				t.Fatalf("rollback writes=%d want=%d", h.f.rollbackRequests, want)
			}
		})
	}
}

// Only completion of the distinct child reconciles its parent. A restored
// parent's ROLLED_BACK status cannot stand in for completion of that child.
func TestLifecycleRollbackChildReconcilesParentOnlyOnCompletion(t *testing.T) {
	for _, status := range []string{"COMPLETED", "ERROR", "ROLLED_BACK", "completed", "FUTURE"} {
		t.Run(status, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = "COMPLETED"
			h.f.mu.Unlock()
			var rb PlanView
			h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rb, 200)
			h.f.mu.Lock()
			h.f.statuses[rb.NativeID] = status
			h.f.statuses[v.NativeID] = "ROLLED_BACK"
			h.f.mu.Unlock()
			h.call("GET", "/api/plans/"+rb.ID, nil, nil, 200)
			parent, err := h.app.store.GetRecord(v.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "COMPLETED"
			if status == "COMPLETED" {
				want = "ROLLED_BACK"
			}
			if parent.Status != want {
				t.Fatalf("parent=%q want=%q after child %q", parent.Status, want, status)
			}
			if len(h.f.executed) != 0 || h.f.rollbackRequests != 1 {
				t.Fatal("refresh dispatched a mutation")
			}
		})
	}
}
