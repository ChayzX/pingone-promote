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
	logger, closeLog, err := logging.Open(logging.Config{Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
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
