package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"
)

type promotionResponseHook struct {
	base       http.RoundTripper
	loseReply  bool
	afterReply func()
}

func (t *promotionResponseHook) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/promotions") {
		return response, err
	}
	if t.afterReply != nil {
		t.afterReply()
	}
	if t.loseReply {
		_ = response.Body.Close()
		return nil, errors.New("fixture connection lost after PingOne accepted the draft")
	}
	return response, nil
}

func installPromotionResponseHook(t *testing.T, h *harness, hook *promotionResponseHook) {
	t.Helper()
	s := h.server.Config.Handler.(*Server)
	originalFactory := s.clientFactory
	s.clients = map[string]*pingone.Client{}
	s.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		client, err := originalFactory(p, secret)
		if err != nil || client == nil {
			return client, err
		}
		if client.HTTP == nil {
			return nil, errors.New("fixture client has no HTTP transport")
		}
		clone := *client.HTTP
		if hook.base == nil {
			hook.base = clone.Transport
		}
		clone.Transport = hook
		client.HTTP = &clone
		return client, nil
	}
}

func requestBodyForNativeDraft(t *testing.T, h *harness) map[string]any {
	t.Helper()
	var source, target struct {
		ID string `json:"id"`
	}
	h.call(http.MethodPost, "/api/inventory", map[string]string{"profileId": h.source.ID}, &source, http.StatusOK)
	h.call(http.MethodPost, "/api/inventory", map[string]string{"profileId": h.target.ID}, &target, http.StatusOK)
	return map[string]any{
		"sourceInventoryId": source.ID,
		"targetInventoryId": target.ID,
		"selected":          []string{"f"},
		"mappings":          map[string]string{"f": "g", "c": "d"},
		"description":       "fixture draft",
	}
}

func assertSafeStorageReadError(t *testing.T, h *harness, path, file, expected string) {
	t.Helper()
	marker := "private-store-marker"
	data := []byte(`{"` + marker + `":true}`)
	if err := os.WriteFile(filepath.Join(h.server.Config.Handler.(*Server).store.Directory(), file), data, 0600); err != nil {
		t.Fatal(err)
	}
	res, err := h.http.Get(h.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET %s returned HTTP %d; want %d", path, res.StatusCode, http.StatusInternalServerError)
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("GET %s did not return a JSON error", path)
	}
	storeDir := h.server.Config.Handler.(*Server).store.Directory()
	if !strings.Contains(result["error"], expected) || strings.Contains(result["error"], marker) || strings.Contains(result["error"], storeDir) {
		t.Fatalf("GET %s returned an unsafe or unactionable storage error", path)
	}
}

func TestProfileReadStorageFailureIsSafeInternalError(t *testing.T) {
	assertSafeStorageReadError(t, setup(t), "/api/profiles", "profiles.json", "Could not read saved profiles; check local app storage")
}

func TestHistoryReadStorageFailureIsSafeInternalError(t *testing.T) {
	assertSafeStorageReadError(t, setup(t), "/api/history", "records.json", "Could not read saved history; check local app storage")
}

func TestPlanLookupDistinguishesMissingRecordFromHistoryReadFailure(t *testing.T) {
	h := setup(t)
	h.call("GET", "/api/plans/record-that-does-not-exist", nil, nil, http.StatusNotFound)
	assertSafeStorageReadError(t, h, "/api/plans/record-that-does-not-exist", "records.json", "Could not read saved history; check local app storage")
}

func assertSafeStorageMutationError(t *testing.T, h *harness, method, path, expected string, body any) {
	t.Helper()
	marker := "private-store-marker"
	data := []byte(`{"` + marker + `":true}`)
	storeDir := h.server.Config.Handler.(*Server).store.Directory()
	if err := os.WriteFile(filepath.Join(storeDir, "profiles.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	req.Header.Set("Origin", h.server.URL)
	res, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	response, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("%s %s returned HTTP %d; want %d", method, path, res.StatusCode, http.StatusInternalServerError)
	}
	var result map[string]string
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatalf("%s %s did not return a JSON error", method, path)
	}
	if !strings.Contains(result["error"], expected) || strings.Contains(result["error"], marker) || strings.Contains(result["error"], storeDir) {
		t.Fatalf("%s %s returned an unsafe or unactionable storage error", method, path)
	}
}

func TestSaveProfileStoreFailureIsSafeInternalError(t *testing.T) {
	h := setup(t)
	assertSafeStorageMutationError(t, h, http.MethodPost, "/api/profiles", "Could not access local app storage", h.target)
}

func TestDeleteProfileStoreFailureIsSafeInternalError(t *testing.T) {
	h := setup(t)
	assertSafeStorageMutationError(t, h, http.MethodDelete, "/api/profiles/"+h.target.ID, "Could not access local app storage", nil)
}

func TestInvalidProfileRemainsClientError(t *testing.T) {
	h := setup(t)
	profile := h.target
	profile.Name = ""
	var result map[string]string
	h.call("POST", "/api/profiles", profile, &result, http.StatusBadRequest)
	if !strings.Contains(result["error"], "profile name is required") {
		t.Fatalf("profile validation error changed: %+v", result)
	}
}

func TestMissingProfileRemainsClientError(t *testing.T) {
	h := setup(t)
	var result map[string]string
	h.call("DELETE", "/api/profiles/missing-profile", nil, &result, http.StatusBadRequest)
	if !strings.Contains(result["error"], "not found") {
		t.Fatalf("missing-profile error changed: %+v", result)
	}
}

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

func TestNativeDraftLostResponseExplainsUncertainCreation(t *testing.T) {
	h := setup(t)
	installPromotionResponseHook(t, h, &promotionResponseHook{loseReply: true})
	var failure map[string]json.RawMessage
	h.call(http.MethodPost, "/api/plans", requestBodyForNativeDraft(t, h), &failure, http.StatusBadGateway)
	var message string
	if err := json.Unmarshal(failure["error"], &message); err != nil {
		t.Fatalf("failure response missing error: %#v", failure)
	}
	for _, expected := range []string{"may have created a draft", "reconcile promotions in PingOne before trying again", "Nothing has been deployed", "PingOne request did not return a response"} {
		if !strings.Contains(message, expected) {
			t.Errorf("uncertain draft error omitted %q: %s", expected, message)
		}
	}
	if _, hasRetryFlag := failure["safeToRetry"]; hasRetryFlag {
		t.Fatal("uncertain native draft failure was marked safe to retry")
	}
	if h.f.created == nil {
		t.Fatal("fixture did not accept the draft before the response was lost")
	}
	var records []struct {
		Status string `json:"status"`
		Intent string `json:"intent"`
	}
	h.call(http.MethodGet, "/api/history", nil, &records, http.StatusOK)
	if len(records) != 1 || records[0].Status != "DRAFT_CREATION_UNCERTAIN" || records[0].Intent != "create_uncertain" {
		t.Fatalf("uncertain native draft was not saved in history: %+v", records)
	}
}

func TestNativeDraftPersistenceFailureExplainsCreatedDraft(t *testing.T) {
	h := setup(t)
	s := h.server.Config.Handler.(*Server)
	storeDir := s.store.Directory()
	recordsPath := filepath.Join(storeDir, "records.json")
	backupPath := filepath.Join(storeDir, "records.before-native-response-test")
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		if err := os.Remove(recordsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, recordsPath); err != nil {
			return err
		}
		restored = true
		return nil
	}
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Errorf("restore fixture history: %v", err)
		}
	})
	afterReply := func() {
		if err := os.Rename(recordsPath, backupPath); err != nil {
			t.Errorf("move fixture history after draft creation: %v", err)
			return
		}
		if err := os.Mkdir(recordsPath, 0700); err != nil {
			t.Errorf("block fixture history save after draft creation: %v", err)
		}
	}
	installPromotionResponseHook(t, h, &promotionResponseHook{afterReply: afterReply})
	var failure map[string]json.RawMessage
	h.call(http.MethodPost, "/api/plans", requestBodyForNativeDraft(t, h), &failure, http.StatusInternalServerError)
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := json.Unmarshal(failure["error"], &message); err != nil {
		t.Fatalf("failure response missing error: %#v", failure)
	}
	lowerMessage := strings.ToLower(message)
	for _, expected := range []string{"PingOne created draft", "p1", "reconcile promotions in PingOne before trying again", "Nothing has been deployed"} {
		if !strings.Contains(lowerMessage, strings.ToLower(expected)) {
			t.Errorf("persist-after-draft error omitted %q: %s", expected, message)
		}
	}
	if strings.Contains(message, storeDir) || strings.Contains(message, "records.json") {
		t.Fatalf("storage details leaked into response: %s", message)
	}
	if h.f.created == nil {
		t.Fatal("fixture did not accept the draft before history persistence failed")
	}
}
