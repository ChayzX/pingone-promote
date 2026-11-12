package pingone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	workerEnv = "99999999-9999-4999-8999-999999999999"
	sourceEnv = "11111111-1111-4111-8111-111111111111"
)

func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Credentials{ClientID: "id", ClientSecret: "secret", WorkerEnvironmentID: workerEnv, RegionCode: "NA"})
	if err != nil {
		t.Fatal(err)
	}
	c.AuthBase, c.APIBase = srv.URL+"/auth", srv.URL+"/api/v1"
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// A busy token endpoint is retried, and a lasting one is reported as
// busy, not as wrong credentials.
func TestTokenThrottlingIsRetried(t *testing.T) {
	var calls atomic.Int32
	var limit atomic.Int32
	limit.Store(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/as/token") {
			if calls.Add(1) <= limit.Load() {
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
			w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		w.Write([]byte(`{"name":"x"}`))
	}))
	defer srv.Close()
	if _, err := testClient(t, srv).Get(context.Background(), sourceEnv, "x"); err != nil {
		t.Fatalf("throttled token was not retried: %v", err)
	}
	calls.Store(0)
	limit.Store(100)
	_, err := testClient(t, srv).Get(context.Background(), sourceEnv, "x")
	if err == nil || !strings.Contains(err.Error(), "busy") || strings.Contains(err.Error(), "check the worker client ID") {
		t.Fatalf("lasting throttling: %v", err)
	}
}

// A TLS proxy the container does not trust is named as such.
func TestUntrustedCertificateIsExplained(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := testClient(t, srv)
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	_, err := c.Get(context.Background(), sourceEnv, "x")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("got %v", err)
	}
}
