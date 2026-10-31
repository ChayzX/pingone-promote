package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pingone-promote/internal/logging"
	"pingone-promote/internal/store"
)

func TestApplicationFailureSharesRequestCorrelationWithoutLoggingPath(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, err := logging.NewController(logging.Config{Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	handler, err := NewWithPolicyAndLogger(st, "bootstrap-token", Policy{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/private-environment-id?token=private-bootstrap-token", nil)
	request.Host = "localhost"
	request.AddCookie(&http.Cookie{Name: "pingone_local", Value: "bootstrap-token"})
	response := httptest.NewRecorder()
	logging.HTTP(logger, handler).ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log records, want app failure and request completion: %s", len(lines), output.String())
	}
	var appFailure, requestEvent map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &appFailure); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &requestEvent); err != nil {
		t.Fatal(err)
	}
	if appFailure["event"] != "app.failure" || appFailure["stage"] != "application" || appFailure["error_class"] != "request_rejected" {
		t.Fatalf("missing application failure fields: %#v", appFailure)
	}
	if appFailure["request_id"] == "" || appFailure["request_id"] != requestEvent["request_id"] {
		t.Fatalf("application and HTTP events are not correlated: %#v %#v", appFailure, requestEvent)
	}
	if strings.Contains(output.String(), "private-environment-id") || strings.Contains(output.String(), "private-bootstrap-token") {
		t.Fatalf("private request data was logged: %s", output.String())
	}
}

func TestLoggingSettingsEndpointIsAuthenticatedAndCSRFProtected(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, err := logging.NewController(logging.Config{}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	handler, err := NewWithPolicyAndLogger(st, "bootstrap-token", Policy{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h := logging.HTTP(logger, handler)
	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/logging", nil)
	unauthenticated.Host = "localhost"
	unauthenticatedResponse := httptest.NewRecorder()
	h.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET returned %d", unauthenticatedResponse.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/logging", nil)
	request.Host = "localhost"
	request.AddCookie(&http.Cookie{Name: "pingone_local", Value: "bootstrap-token"})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/logging returned %d: %s", response.Code, response.Body.String())
	}
	var settings map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["destination"] != "stderr" || settings["format"] != "json" || settings["debug"] != false || settings["level"] != "info" {
		t.Fatalf("unexpected effective settings: %#v", settings)
	}
	put := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/logging", strings.NewReader(`{"debug":true,"level":"debug","format":"text","destination":"stderr","file":""}`))
		r.Host = "localhost"
		r.AddCookie(&http.Cookie{Name: "pingone_local", Value: "bootstrap-token"})
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if response := put(""); response.Code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF token returned %d", response.Code)
	}
	if response := put(handler.csrf); response.Code != http.StatusOK {
		t.Fatalf("PUT with CSRF token returned %d: %s", response.Code, response.Body.String())
	}
	if got := logger.Settings(); !got.Debug || got.Level != "debug" || got.Format != "text" {
		t.Fatalf("authenticated settings change was not applied: %+v", got)
	}
	before := logger.Settings()
	invalid := httptest.NewRequest(http.MethodPut, "/api/logging", strings.NewReader(`{"debug":false,"level":"info","format":"json","destination":"file","file":"relative.log"}`))
	invalid.Host = "localhost"
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("X-CSRF-Token", handler.csrf)
	invalid.AddCookie(&http.Cookie{Name: "pingone_local", Value: "bootstrap-token"})
	invalidResponse := httptest.NewRecorder()
	h.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid file destination returned %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	}
	if got := logger.Settings(); got != before {
		t.Fatalf("failed UI sink change altered effective settings: before=%+v after=%+v", before, got)
	}
	if strings.Contains(output.String(), "relative.log") || strings.Contains(output.String(), handler.csrf) {
		t.Fatalf("logging settings path or CSRF token was logged: %s", output.String())
	}
}
