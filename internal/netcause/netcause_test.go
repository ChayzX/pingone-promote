package netcause

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUntrustedCertificateIsNamed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := http.Get(srv.URL) // the default client does not trust the test CA
	if err == nil {
		t.Fatal("expected a TLS error")
	}
	if got := Describe(err, "api.example.com"); !strings.Contains(got, "not trusted") || !strings.Contains(got, "extra-ca") {
		t.Fatalf("got %q", got)
	}
}

func TestRefusedAndDNS(t *testing.T) {
	_, err := http.Get("http://127.0.0.1:1/")
	if got := Describe(err, "127.0.0.1"); !strings.Contains(got, "refused") && !strings.Contains(got, "failed") {
		t.Fatalf("got %q", got)
	}
	_, err = http.Get("http://no-such-host.invalid/")
	if got := Describe(err, "no-such-host.invalid"); !strings.Contains(got, "DNS") && !strings.Contains(got, "failed") {
		t.Fatalf("got %q", got)
	}
}
