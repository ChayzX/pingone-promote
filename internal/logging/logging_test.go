package logging

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedBuffer) String() string {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.String()
}

func TestHTTPLogsAllowlistedRouteAndOmitsSensitiveRequestData(t *testing.T) {
	var output lockedBuffer
	logger, closeLog, err := Open(Config{Level: "info", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	const secret = "sentinel-secret-credential"
	h := HTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	request := httptest.NewRequest("POST", "/api/profiles/private-tenant-id?token="+secret, strings.NewReader("body-personal-data"))
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("request correlation header missing")
	}
	line := strings.TrimSpace(output.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("invalid JSON log: %v", err)
	}
	if record["event"] != "http.request" || record["stage"] != "http" || record["route"] != "api.profile" || record["method"] != "POST" || record["error_class"] != "request_rejected" {
		t.Fatalf("missing structured event fields: %#v", record)
	}
	if strings.Contains(line, secret) || strings.Contains(line, "private-tenant-id") || strings.Contains(line, "body-personal-data") || strings.Contains(line, "Authorization") {
		t.Fatalf("sensitive request data was logged: %s", line)
	}
}

func TestHTTPFailureLogsStageWithoutNativeIdentifierOrErrorText(t *testing.T) {
	var output lockedBuffer
	logger, closeLog, err := Open(Config{Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	const nativeID = "native-promotion-private-id"
	h := HTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "synthetic private tenant detail", http.StatusBadGateway)
	}))
	r := httptest.NewRequest("POST", "/api/plans/"+nativeID+"/deploy", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	line := strings.TrimSpace(output.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("invalid JSON log: %v", err)
	}
	if record["level"] != "ERROR" || record["event"] != "http.request" || record["stage"] != "http" || record["route"] != "api.plans" || record["method"] != "POST" || record["error_class"] != "request_failed" {
		t.Fatalf("missing failure stage fields: %#v", record)
	}
	if strings.Contains(line, nativeID) || strings.Contains(line, "synthetic private tenant detail") {
		t.Fatalf("private identifier or error text was logged: %s", line)
	}
}

func TestDebugHTTPEventsExcludeRequestPayloadAndCredentials(t *testing.T) {
	var output lockedBuffer
	controller, err := NewController(Config{Level: "debug", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	h := HTTP(controller, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	const sentinel = "fixture-secret-tenant-personal-data"
	r := httptest.NewRequest("POST", "/api/plans/native-private-id/deploy?token="+sentinel, strings.NewReader("native-payload-"+sentinel))
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("X-CSRF-Token", sentinel)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("debug mode should emit dispatch and completion only: %s", output.String())
	}
	var dispatch, completion map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &dispatch); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &completion); err != nil {
		t.Fatal(err)
	}
	if dispatch["level"] != "DEBUG" || dispatch["event"] != "http.dispatch" || dispatch["route"] != "api.plans" || dispatch["method"] != "POST" {
		t.Fatalf("debug dispatch missing bounded fields: %#v", dispatch)
	}
	if dispatch["request_id"] == "" || dispatch["request_id"] != completion["request_id"] {
		t.Fatalf("debug and completion events are not correlated: %#v %#v", dispatch, completion)
	}
	for _, private := range []string{sentinel, "native-private-id", "native-payload", "Authorization", "X-CSRF-Token"} {
		if strings.Contains(output.String(), private) {
			t.Fatalf("debug logs contained %q: %s", private, output.String())
		}
	}
}

func TestHTTPRequestCorrelationIsUniqueUnderConcurrency(t *testing.T) {
	var output lockedBuffer
	logger, closeLog, err := Open(Config{Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	h := HTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	const count = 80
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/api/session", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			ids <- w.Header().Get("X-Request-ID")
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("missing or duplicate request id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d ids, want %d", len(seen), count)
	}
	logged := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON log: %v", err)
		}
		id, _ := record["request_id"].(string)
		if !seen[id] || logged[id] {
			t.Fatalf("log request id %q is absent from or duplicated across responses", id)
		}
		logged[id] = true
	}
	if len(logged) != count {
		t.Fatalf("got %d logged request ids, want %d", len(logged), count)
	}
}

func TestOpenRejectsInvalidConfigAndCreatesPrivateLog(t *testing.T) {
	if _, _, err := Open(Config{Level: "verbose"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected invalid level error")
	}
	path := t.TempDir() + "/nested/app.log"
	if runtime.GOOS == "windows" {
		if _, _, err := Open(Config{File: path}, &bytes.Buffer{}); err == nil {
			t.Fatal("expected Windows file logging to be rejected without a private ACL")
		}
		return
	}
	logger, closeLog, err := Open(Config{File: path}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	logger.Event(nil, 0, "http.request", "http", "api.session", "", "id", "", 200, 0)
	if err := closeLog(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("log mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOpenRejectsSymlinkLogDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file logging is deliberately unavailable on Windows")
	}
	dir := t.TempDir()
	target := dir + "/target.log"
	link := dir + "/linked.log"
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := Open(Config{File: link}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected symlink log destination rejection")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep" {
		t.Fatalf("target content changed: %q", content)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, os.ErrPermission }

func TestFileWriterFallsBackToStderrOnWriteFailure(t *testing.T) {
	var fallback bytes.Buffer
	w := fallbackWriter{primary: failedWriter{}, fallback: &fallback}
	line := []byte("safe structured event\n")
	n, err := w.Write(line)
	if err != nil || n != len(line) {
		t.Fatalf("fallback write = %d, %v", n, err)
	}
	if !strings.Contains(fallback.String(), string(line)) || !strings.Contains(fallback.String(), "structured log file write failed") {
		t.Fatalf("fallback did not preserve event: %q", fallback.String())
	}
}

func TestEventFieldsAreRestrictedToAllowlistedValues(t *testing.T) {
	var output bytes.Buffer
	logger, closeLog, err := Open(Config{Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	logger.Event(nil, 0, "private-tenant-id", "bootstrap-token", "/api/profiles/private-id", "credential-method", "personal-request-id", "client-secret", 500, 1)
	line := strings.TrimSpace(output.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "event" || record["stage"] != "unknown" || record["route"] != "api.other" || record["method"] != "OTHER" || record["error_class"] != "operation_failed" {
		t.Fatalf("untrusted logging fields were not constrained: %#v", record)
	}
	for _, private := range []string{"private-tenant-id", "bootstrap-token", "private-id", "credential-method", "personal-request-id", "client-secret"} {
		if strings.Contains(line, private) {
			t.Fatalf("untrusted value %q was logged: %s", private, line)
		}
	}
}
