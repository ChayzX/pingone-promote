package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type futureDraftStatusTransport struct {
	base    http.RoundTripper
	mu      sync.Mutex
	created bool
}

func (tr *futureDraftStatusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	created := tr.created
	tr.mu.Unlock()
	if created && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/promotions/") {
		return nil, errors.New("synthetic observation unavailable")
	}
	response, err := tr.base.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/promotions") {
		return response, err
	}
	b, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err = json.Unmarshal(b, &body); err != nil {
		return nil, err
	}
	body["status"] = "FUTURE_PRIVATE_STATUS_secret"
	b, err = json.Marshal(body)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(b))
	response.ContentLength = int64(len(b))
	tr.mu.Lock()
	tr.created = true
	tr.mu.Unlock()
	return response, nil
}

func evidenceObject(t *testing.T, value any) map[string]any {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertAppEvidence(t *testing.T, got map[string]any, dispatch string, observed bool) {
	t.Helper()
	if got["dispatchState"] != dispatch || got["nativeObserved"] != observed || got["effectsVerified"] != false {
		t.Fatalf("dispatch, observation and effects evidence incomplete: %+v", got)
	}
}

func TestAppDraftObservationDoesNotClaimDispatch(t *testing.T) {
	h := setup(t)
	v := h.plan()
	assertAppEvidence(t, evidenceObject(t, v), "not_dispatched", true)
	if len(executedIDs(h.f)) != 0 {
		t.Fatal("read-only observation executed a promotion")
	}
}

func TestAppCompletedExecutionEvidenceRemainsUnverified(t *testing.T) {
	h := setup(t)
	v := h.plan()
	var result map[string]any
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &result, http.StatusOK)
	assertAppEvidence(t, result, "identified", true)
	if result["status"] != "COMPLETED" {
		t.Fatalf("native completion missing: %+v", result)
	}
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := evidenceObject(t, record)["execution"].(map[string]any)
	if !ok {
		t.Fatal("no durable execution evidence")
	}
	assertAppEvidence(t, evidence, "identified", true)
	if evidence["version"] != float64(1) {
		t.Fatalf("unversioned evidence: %+v", evidence)
	}
	if len(executedIDs(h.f)) != 1 {
		t.Fatal("unexpected execution count")
	}
}

func TestAppLostExecutionResponseRetainsUncertainEvidence(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.failExecuteAfterWrite = true
	h.f.mu.Unlock()
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusBadGateway)
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := evidenceObject(t, record)["execution"].(map[string]any)
	if !ok {
		t.Fatal("uncertain execution has no durable evidence")
	}
	assertAppEvidence(t, evidence, "uncertain", false)
	var current map[string]any
	h.call(http.MethodGet, "/api/plans/"+v.ID, nil, &current, http.StatusOK)
	assertAppEvidence(t, current, "uncertain", true)
	if current["needsReconciliation"] != true || current["canDeploy"] != false {
		t.Fatalf("uncertainty became executable: %+v", current)
	}
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusConflict)
	if len(executedIDs(h.f)) != 1 {
		t.Fatal("uncertain execution replayed")
	}
}

func TestAppUnknownStatusIsSafeUnverifiedObservation(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.statuses[v.NativeID] = "FUTURE_PRIVATE_STATUS_secret"
	h.f.mu.Unlock()
	var current map[string]any
	h.call(http.MethodGet, "/api/plans/"+v.ID, nil, &current, http.StatusOK)
	if current["status"] != "UNKNOWN" || current["needsReconciliation"] != true || current["canDeploy"] != false {
		t.Fatalf("unknown state was trusted: %+v", current)
	}
	assertAppEvidence(t, current, "not_dispatched", true)
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(record)
	if strings.Contains(string(b), "FUTURE_PRIVATE_STATUS_secret") {
		t.Fatal("raw unknown native status persisted")
	}
}

func TestAppLegacyExecutionIntentRemainsConservative(t *testing.T) {
	h := setup(t)
	v := h.plan()
	ps := h.app.plans[v.ID]
	ps.Record.Intent = "execute_sent"
	if err := h.app.store.SaveRecord(ps.Record); err != nil {
		t.Fatal(err)
	}
	var current map[string]any
	h.call(http.MethodGet, "/api/plans/"+v.ID, nil, &current, http.StatusOK)
	assertAppEvidence(t, current, "uncertain", true)
	if current["canDeploy"] != false || current["needsReconciliation"] != true {
		t.Fatalf("legacy intent gained permission: %+v", current)
	}
	if len(executedIDs(h.f)) != 0 {
		t.Fatal("legacy inspection dispatched")
	}
}

func TestAppExecutionIdentityCheckpointPrecedesPolling(t *testing.T) {
	h := setup(t)
	v := h.plan()
	observations := 0
	h.f.mu.Lock()
	h.f.beforeExecutedStatus = func() {
		observations++
		record, err := h.app.store.GetRecord(v.ID)
		if err != nil {
			t.Error(err)
			return
		}
		evidence, ok := evidenceObject(t, record)["execution"].(map[string]any)
		if !ok || evidence["dispatchState"] != "identified" || evidence["nativeObserved"] != true || evidence["effectsVerified"] != false {
			t.Errorf("polling preceded durable matching-identity checkpoint: %+v", evidence)
		}
	}
	h.f.mu.Unlock()
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusOK)
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if observations == 0 {
		t.Fatal("test did not exercise post-dispatch polling")
	}
}

func TestAppMissingExecutionResponseIdentityRetainsUncertainty(t *testing.T) {
	h := setup(t)
	v := h.plan()
	empty := ""
	postReads := 0
	h.f.mu.Lock()
	h.f.executeIDOverride = &empty
	h.f.beforeExecutedStatus = func() { postReads++ }
	h.f.mu.Unlock()
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusBadGateway)
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := evidenceObject(t, record)["execution"].(map[string]any)
	if !ok {
		t.Fatal("missing response identity lost uncertain evidence")
	}
	assertAppEvidence(t, evidence, "uncertain", false)
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if postReads != 0 || len(h.f.executed) != 1 {
		t.Fatal("untrusted identity was polled or execution replayed")
	}
}

func TestAppExecutionCheckpointFailureRetainsUncertainRecord(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires supported-platform owner permission enforcement")
	}
	h := setup(t)
	v := h.plan()
	dir := h.app.store.Directory()
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	postReads := 0
	h.f.mu.Lock()
	h.f.afterExecute = func() {
		if err := os.Chmod(dir, 0500); err != nil {
			t.Error(err)
		}
	}
	h.f.beforeExecutedStatus = func() { postReads++ }
	h.f.mu.Unlock()
	var failure failureEnvelope
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &failure, http.StatusInternalServerError)
	if failure.Code != "execution_identity_checkpoint_failed" || failure.RecoveryAction != "reconcile_native_status" {
		t.Fatalf("checkpoint failure lost recovery contract: %+v", failure)
	}
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := evidenceObject(t, record)["execution"].(map[string]any)
	if !ok {
		t.Fatal("checkpoint failure lost earlier intent")
	}
	assertAppEvidence(t, evidence, "uncertain", false)
	h.f.mu.Lock()
	reads := postReads
	h.f.mu.Unlock()
	if reads != 0 {
		t.Fatal("polled after failed identity checkpoint")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, http.StatusConflict)
	if len(executedIDs(h.f)) != 1 {
		t.Fatal("failed checkpoint permitted replay")
	}
}

func TestAppCreatedDraftUnknownStatusDoesNotEscapeAfterRefreshFailure(t *testing.T) {
	for _, kind := range []string{"promotion", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			h := setup(t)
			var path string
			var body any
			if kind == "rollback" {
				v := h.plan()
				h.f.mu.Lock()
				h.f.statuses[v.NativeID] = "COMPLETED"
				h.f.mu.Unlock()
				path, body = "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}
			} else {
				var source, target struct {
					ID string `json:"id"`
				}
				h.call("POST", "/api/inventory", map[string]string{"profileId": h.source.ID}, &source, 200)
				h.call("POST", "/api/inventory", map[string]string{"profileId": h.target.ID}, &target, 200)
				path, body = "/api/plans", map[string]any{"sourceInventoryId": source.ID, "targetInventoryId": target.ID, "selected": []string{"f"}, "mappings": map[string]string{"f": "g", "c": "d"}}
			}
			client := h.app.clients[h.source.ID]
			copy := *client.HTTP
			copy.Transport = &futureDraftStatusTransport{base: copy.Transport}
			client.HTTP = &copy
			h.call("POST", path, body, nil, 502)
			records, err := h.app.store.ListRecords()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range records {
				if record.Kind == kind && record.NativeID != "" {
					found = true
					if record.Status != "UNKNOWN" {
						t.Fatalf("checkpoint exposed untrusted status: %q", record.Status)
					}
				}
			}
			if !found {
				t.Fatal("accepted draft identity was lost")
			}
			var history []map[string]any
			h.call("GET", "/api/history", nil, &history, 200)
			b, _ := json.Marshal(history)
			if strings.Contains(string(b), "FUTURE_PRIVATE_STATUS_secret") {
				t.Fatal("history exposed raw status")
			}
		})
	}
}

func TestAppLegacyHistoryProjectsUnknownStatusWithoutRewritingEvidence(t *testing.T) {
	h := setup(t)
	v := h.plan()
	record, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Status = "LEGACY_PRIVATE_STATUS_secret"
	if err = h.app.store.SaveRecord(record); err != nil {
		t.Fatal(err)
	}
	var history []map[string]any
	h.call("GET", "/api/history", nil, &history, 200)
	for _, row := range history {
		if row["id"] == v.ID && row["status"] != "UNKNOWN" {
			t.Fatalf("legacy history exposed unknown status: %+v", row)
		}
	}
	restored, err := h.app.store.GetRecord(v.ID)
	if err != nil || restored.Status != record.Status {
		t.Fatal("read-only history rewrote retained evidence")
	}
}

func TestAppUnsupportedExecutionEvidenceBlocksAllMutation(t *testing.T) {
	for _, field := range []string{"version", "dispatchState", "nativeObserved", "effectsVerified"} {
		t.Run(field, func(t *testing.T) {
			h := setup(t)
			v := h.plan()
			metadata := map[string]any{"version": 1, "dispatchState": "identified", "nativeObserved": true, "effectsVerified": false}
			switch field {
			case "version":
				metadata[field] = 99
			case "dispatchState":
				metadata[field] = "future"
			case "nativeObserved":
				metadata[field] = false
			case "effectsVerified":
				metadata[field] = true
			}
			ps := h.app.plans[v.ID]
			raw := evidenceObject(t, ps.Record)
			raw["execution"] = metadata
			b, _ := json.Marshal(raw)
			if err := json.Unmarshal(b, &ps.Record); err != nil {
				t.Fatal(err)
			}
			if err := h.app.store.SaveRecord(ps.Record); err != nil {
				t.Fatal(err)
			}
			var current map[string]any
			h.call("GET", "/api/plans/"+v.ID, nil, &current, 200)
			b, _ = json.Marshal(current["review"])
			if current["canDeploy"] != false || current["needsReconciliation"] != true || !strings.Contains(string(b), "execution evidence") {
				t.Fatalf("unsupported execution evidence was not explained and blocked: %+v", current)
			}
			var history []map[string]any
			h.call("GET", "/api/history", nil, &history, 200)
			for _, row := range history {
				if row["id"] == v.ID {
					evidence := row["execution"].(map[string]any)
					if evidence["effectsVerified"] != false {
						t.Fatal("history trusted a disk-only effects claim")
					}
				}
			}
			h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
			h.f.mu.Lock()
			h.f.statuses[v.NativeID] = "COMPLETED"
			h.f.mu.Unlock()
			h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, nil, 409)
			if len(executedIDs(h.f)) != 0 || rollbackRequestCount(h.f) != 0 {
				t.Fatal("unsupported evidence dispatched a mutation")
			}
		})
	}
}
