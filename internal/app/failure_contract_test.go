package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http"
	"net/http/httptest"
	"pingone-promote/internal/pingone"
)

type failureEnvelope struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	Phase           string `json:"phase"`
	Message         string `json:"message"`
	RecoveryAction  string `json:"recoveryAction"`
	CorrelationID   string `json:"correlationId"`
	MutationStarted *bool  `json:"mutationStarted"`
	SafeToRetry     *bool  `json:"safeToRetry"`
}

func TestUncertainExecutionReturnsSafeClassifiedFailure(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.failExecuteAfterWrite = true
	h.f.executeFailureBody = `{"code":"PRIVATE_VENDOR_CODE","message":"private tenant secret=vendor-secret"}`
	h.f.mu.Unlock()

	var got failureEnvelope
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &got, http.StatusBadGateway)
	if got.Code != "execution_outcome_uncertain" || got.Phase != "execution" || got.RecoveryAction != "reconcile_native_status" {
		t.Fatalf("uncertain execution failure was misclassified: %+v", got)
	}
	if got.Message == "" || got.Error != got.Message || got.CorrelationID == "" {
		t.Fatalf("failure contract is incomplete or incompatible: %+v", got)
	}
	for _, secret := range []string{"PRIVATE_VENDOR_CODE", "private tenant", "vendor-secret"} {
		if strings.Contains(got.Error, secret) || strings.Contains(got.Message, secret) {
			t.Fatalf("failure exposed vendor detail %q: %+v", secret, got)
		}
	}
	if len(executedIDs(h.f)) != 1 {
		t.Fatalf("execution writes=%d want 1", len(executedIDs(h.f)))
	}
}

func TestPreflightFailureClassifiesNoWriteWithoutExposingCause(t *testing.T) {
	h := setup(t)
	v := h.plan()
	s := h.server.Config.Handler.(*Server)
	s.mu.Lock()
	delete(s.clients, h.source.ID)
	s.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		return nil, errors.New("fixture client setup failed: secret=private-client-data")
	}
	s.mu.Unlock()

	var got failureEnvelope
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &got, http.StatusBadGateway)
	if got.Code != "pre_dispatch_failure" || got.Phase != "preflight" || got.RecoveryAction != "retry_pre_dispatch" {
		t.Fatalf("preflight failure was misclassified: %+v", got)
	}
	if got.Message == "" || got.Error != got.Message || got.CorrelationID == "" {
		t.Fatalf("failure contract is incomplete or incompatible: %+v", got)
	}
	if got.MutationStarted == nil || *got.MutationStarted || got.SafeToRetry == nil || !*got.SafeToRetry {
		t.Fatalf("preflight failure did not retain its no-write compatibility fields: %+v", got)
	}
	if strings.Contains(got.Error, "fixture client setup failed") || strings.Contains(got.Error, "private-client-data") {
		t.Fatalf("preflight failure exposed internal cause: %+v", got)
	}
}

func TestRefreshClientSetupFailureUsesSafeFailureProjection(t *testing.T) {
	h := setup(t)
	v := h.plan()
	s := h.server.Config.Handler.(*Server)
	s.mu.Lock()
	delete(s.clients, h.source.ID)
	s.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		return nil, errors.New("fixture client setup failed: secret=private-client-data")
	}
	s.mu.Unlock()

	var got failureEnvelope
	h.call(http.MethodGet, "/api/plans/"+v.ID, nil, &got, http.StatusBadGateway)
	if got.Code != "client_setup_failed" || got.Phase != "preflight" || got.RecoveryAction != "retry_pre_dispatch" {
		t.Fatalf("client setup failure was misclassified: %+v", got)
	}
	if got.Error != got.Message || got.CorrelationID == "" || strings.Contains(got.Error, "private-client-data") || strings.Contains(got.Message, "fixture client setup failed") {
		t.Fatalf("client setup response was unsafe or incomplete: %+v", got)
	}
}

func TestGenericClientFailureDoesNotExposeDiagnostic(t *testing.T) {
	w := httptest.NewRecorder()
	response := `C:\private\app-data\profiles.json access token=private-diagnostic`
	replyError(w, http.StatusBadRequest, errors.New(response))

	var got failureEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Error, response) || strings.Contains(got.Message, "private-diagnostic") || strings.Contains(got.Message, "profiles.json") {
		t.Fatalf("generic client failure exposed diagnostic: %+v", got)
	}
	if got.Code != "request_rejected" || got.Error == "" || got.Error != got.Message {
		t.Fatalf("generic failure did not use stable safe projection: %+v", got)
	}
}

func TestDecodeBodyDoesNotEchoUnknownJSONField(t *testing.T) {
	h := setup(t)
	var got failureEnvelope
	h.call(http.MethodPost, "/api/inventory", map[string]string{
		"profileId":                      h.source.ID,
		"private-token=synthetic-secret": "value",
	}, &got, http.StatusBadRequest)
	if strings.Contains(got.Error, "private-token") || strings.Contains(got.Error, "synthetic-secret") {
		t.Fatalf("JSON decoder echoed an untrusted field name: %+v", got)
	}
	if !strings.Contains(strings.ToLower(got.Error), "json") {
		t.Fatalf("JSON validation guidance is not actionable: %+v", got)
	}
}

func TestMissingProfileCredentialReturnsSafePreDispatchGuidance(t *testing.T) {
	h := setup(t)
	t.Setenv("TEST_PING_SECRET", "")
	var got failureEnvelope
	h.call(http.MethodPost, "/api/inventory", map[string]string{"profileId": h.source.ID}, &got, http.StatusBadRequest)
	if got.Code != "credential_unavailable" || got.Phase != "preflight" || got.RecoveryAction != "retry_pre_dispatch" {
		t.Fatalf("missing credential was not classified as pre-dispatch: %+v", got)
	}
	if !strings.Contains(strings.ToLower(got.Message), "no pingone write was sent") || strings.Contains(got.Message, "secret") {
		t.Fatalf("missing credential guidance is unsafe or incomplete: %+v", got)
	}
}

func TestMappingWarningDoesNotExposePingOneDiagnostic(t *testing.T) {
	h := setup(t)
	h.f.mu.Lock()
	h.f.mappingFailureBody = `{"code":"PRIVATE_VENDOR_CODE","message":"tenant private token=synthetic-secret"}`
	h.f.mu.Unlock()
	var source, target struct {
		ID string `json:"id"`
	}
	h.call(http.MethodPost, "/api/inventory", map[string]string{"profileId": h.source.ID}, &source, http.StatusOK)
	h.call(http.MethodPost, "/api/inventory", map[string]string{"profileId": h.target.ID}, &target, http.StatusOK)
	var got struct {
		Warnings []string `json:"warnings"`
	}
	h.call(http.MethodPost, "/api/mappings", map[string]string{"sourceInventoryId": source.ID, "targetInventoryId": target.ID}, &got, http.StatusOK)
	serialized, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range []string{"PRIVATE_VENDOR_CODE", "tenant private", "synthetic-secret"} {
		if strings.Contains(string(serialized), diagnostic) {
			t.Fatalf("mapping warning exposed PingOne diagnostic %q: %s", diagnostic, serialized)
		}
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "Saved PingOne mappings could not be read") {
		t.Fatalf("mapping warning lost actionable application guidance: %+v", got.Warnings)
	}
}

func TestCompletedRollbackChildSurfacesParentReconciliationFailure(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.statuses[v.NativeID] = "COMPLETED"
	h.f.mu.Unlock()
	var rollback PlanView
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rollback, http.StatusOK)

	h.f.mu.Lock()
	h.f.statuses[rollback.NativeID] = "COMPLETED"
	h.f.failParentStatusRead = true
	h.f.mu.Unlock()
	var got failureEnvelope
	h.call(http.MethodGet, "/api/plans/"+rollback.ID, nil, &got, http.StatusBadGateway)
	if got.Code != "rollback_reconciliation_incomplete" || got.Phase != "reconciliation" || got.RecoveryAction != "refresh_native_status" {
		t.Fatalf("parent reconciliation failure was misclassified: %+v", got)
	}
	if got.Error != got.Message || got.CorrelationID == "" {
		t.Fatalf("failure contract is incomplete or incompatible: %+v", got)
	}
	for _, secret := range []string{"PRIVATE_VENDOR_CODE", "private tenant", "vendor-secret"} {
		if strings.Contains(got.Error, secret) || strings.Contains(got.Message, secret) {
			t.Fatalf("failure exposed vendor detail %q: %+v", secret, got)
		}
	}
	child, err := h.app.store.GetRecord(rollback.ID)
	if err != nil || child.Status != "COMPLETED" {
		t.Fatalf("observed rollback child completion was lost: record=%+v err=%v", child, err)
	}
}

func TestCompletedRollbackChildSurfacesMissingParentFailure(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.statuses[v.NativeID] = "COMPLETED"
	h.f.mu.Unlock()
	var rollback PlanView
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rollback, http.StatusOK)

	child := h.app.plans[rollback.ID]
	child.Record.ParentID = "missing-parent-record"
	if err := h.app.store.SaveRecord(child.Record); err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	h.f.statuses[rollback.NativeID] = "COMPLETED"
	h.f.mu.Unlock()
	var got failureEnvelope
	h.call(http.MethodGet, "/api/plans/"+rollback.ID, nil, &got, http.StatusBadGateway)
	if got.Code != "rollback_reconciliation_incomplete" || got.RecoveryAction != "refresh_native_status" || strings.Contains(got.Error, "missing-parent-record") {
		t.Fatalf("missing parent failure was not safely surfaced: %+v", got)
	}
	if child.Record.Status != "COMPLETED" {
		t.Fatalf("observed rollback child completion was lost: %+v", child.Record)
	}
}

func TestCompletedRollbackChildSurfacesParentPersistenceFailure(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.statuses[v.NativeID] = "COMPLETED"
	h.f.mu.Unlock()
	var rollback PlanView
	h.call(http.MethodPost, "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rollback, http.StatusOK)
	child := h.app.plans[rollback.ID]
	h.f.mu.Lock()
	h.f.statuses[rollback.NativeID] = "COMPLETED"
	h.f.afterParentStatus = func() {
		recordsPath := filepath.Join(h.app.store.Directory(), "records.json")
		backupPath := filepath.Join(h.app.store.Directory(), "records.saved-for-parent-failure")
		if err := os.Rename(recordsPath, backupPath); err != nil {
			t.Errorf("move history after parent status read: %v", err)
			return
		}
		if err := os.Mkdir(recordsPath, 0700); err != nil {
			t.Errorf("inject parent history save failure: %v", err)
			_ = os.Rename(backupPath, recordsPath)
		}
		t.Cleanup(func() {
			if info, err := os.Stat(recordsPath); err == nil && info.IsDir() {
				_ = os.Remove(recordsPath)
			}
			if _, err := os.Stat(backupPath); err == nil {
				_ = os.Rename(backupPath, recordsPath)
			}
		})
	}
	h.f.mu.Unlock()
	var got failureEnvelope
	h.call(http.MethodGet, "/api/plans/"+rollback.ID, nil, &got, http.StatusInternalServerError)
	if got.Code != "rollback_reconciliation_incomplete" || got.RecoveryAction != "refresh_native_status" || strings.Contains(got.Error, h.app.store.Directory()) {
		t.Fatalf("parent persistence failure was not safely surfaced: %+v", got)
	}
	if child.Record.Status != "COMPLETED" {
		t.Fatalf("observed rollback child completion was lost: %+v", child.Record)
	}
}
