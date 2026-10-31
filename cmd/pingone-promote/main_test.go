package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestListenAddressCannotExposeCredentialInterface(t *testing.T) {
	for _, addr := range []string{":8787", "0.0.0.0:8787", "[::]:8787", "example.com:8787", "localhost:bad"} {
		if err := validateListen(addr); err == nil {
			t.Fatalf("accepted unsafe listen %q", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787"} {
		if err := validateListen(addr); err != nil {
			t.Fatalf("rejected loopback %q: %v", addr, err)
		}
	}
}

func TestVersionHelperProcess(t *testing.T) {
	if os.Getenv("PINGONE_VERSION_HELPER") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("pingone-promote", flag.ExitOnError)
	os.Args = []string{"pingone-promote", "--version", "--listen", "invalid", "--data-dir", os.Getenv("PINGONE_VERSION_DIRECTORY")}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// A version request must succeed before listen validation or data directory creation.
func TestVersionPrintsAndSkipsApplicationStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app-data")
	cmd := exec.Command(os.Args[0], "-test.run=^TestVersionHelperProcess$")
	cmd.Env = append(os.Environ(), "PINGONE_VERSION_HELPER=1", "PINGONE_VERSION_DIRECTORY="+dir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("version command failed: %v, output=%q", err, output)
	}
	if got, want := string(output), "pingone-promote dev\n"; got != want {
		t.Fatalf("version output=%q, want %q", got, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("version command created data directory: stat err=%v", err)
	}
}

func TestDataDirectoryAllowsOnlyOneApplicationInstance(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireLock(dir)
	if err == nil {
		second.Close()
		t.Fatal("concurrent process could modify the same data directory")
	}
}

func TestShutdownHelperProcess(t *testing.T) {
	if os.Getenv("PINGONE_SHUTDOWN_HELPER") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("pingone-promote", flag.ExitOnError)
	os.Args = []string{"pingone-promote", "--no-browser", "--listen", "127.0.0.1:0", "--data-dir", os.Getenv("PINGONE_SHUTDOWN_DIRECTORY")}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Closing the listener must not exit the process before Shutdown drains active
// handlers, or an in-flight profile/history write can be interrupted.
func TestShutdownWaitsForActiveRequest(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownHelperProcess$")
	cmd.Env = append(os.Environ(), "PINGONE_SHUTDOWN_HELPER=1", "PINGONE_SHUTDOWN_DIRECTORY="+t.TempDir())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Open: ") {
				ready <- strings.TrimPrefix(scanner.Text(), "Open: ")
				return
			}
		}
		ready <- ""
	}()
	var startup string
	select {
	case startup = <-ready:
		if startup == "" {
			t.Fatal("helper exited without a startup URL")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not start")
	}
	u, err := url.Parse(startup)
	if err != nil {
		t.Fatal(err)
	}
	cookie := "pingone_local=" + u.Query().Get("token")
	req, _ := http.NewRequest(http.MethodGet, "http://"+u.Host+"/api/session", nil)
	req.Header.Set("Cookie", cookie)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	err = json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if err != nil || session.CSRF == "" {
		t.Fatalf("load local session: %v", err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	body := `{"name":"Graceful profile","region":"us","environmentId":"test-env","clientId":"test-client","secretMode":"session"}`
	_, err = fmt.Fprintf(conn, "POST /api/profiles HTTP/1.1\r\nHost: %s\r\nCookie: %s\r\nX-CSRF-Token: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", u.Host, cookie, session.CSRF, len(body))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	interim, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil || interim.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not begin reading the body: response=%v error=%v", interim, err)
	}
	interim.Body.Close()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("process exited before its active request completed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := io.WriteString(conn, body); err != nil {
		t.Fatal(err)
	}
	completed, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil || completed.StatusCode != http.StatusOK {
		t.Fatalf("active request did not finish during shutdown: response=%v error=%v", completed, err)
	}
	io.Copy(io.Discard, completed.Body)
	completed.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit after draining its request")
	}
}
