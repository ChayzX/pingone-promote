package server

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestDemoServer runs the server against the fake tenant, fake terraformer
// and fake GitLab on a fixed local address, for looking at the pages in a
// browser. It only runs when PINGONE_PROMOTE_DEMO_ADDR is set, for example
//
//	PINGONE_PROMOTE_DEMO_ADDR=127.0.0.1:8787 go test -run TestDemoServer -timeout 0 ./internal/server
//
// and stops when the file named by PINGONE_PROMOTE_DEMO_STOP appears (or
// after two hours).
func TestDemoServer(t *testing.T) {
	addr := os.Getenv("PINGONE_PROMOTE_DEMO_ADDR")
	if addr == "" {
		t.Skip("set PINGONE_PROMOTE_DEMO_ADDR to run the demo server")
	}
	e := newCoreEnv(t)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	t.Logf("demo server on http://%s", addr)
	stop := os.Getenv("PINGONE_PROMOTE_DEMO_STOP")
	for deadline := time.Now().Add(2 * time.Hour); time.Now().Before(deadline); time.Sleep(time.Second) {
		e.clock.advance(time.Second)
		if stop != "" {
			if _, err := os.Stat(stop); err == nil {
				return
			}
		}
	}
}
