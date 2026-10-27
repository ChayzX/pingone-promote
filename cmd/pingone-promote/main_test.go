package main

import "testing"

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
