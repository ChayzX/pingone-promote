// PingOne Promote is a local, explicitly scoped configuration-promotion UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"pingone-promote/internal/app"
	"pingone-promote/internal/store"
)

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("--listen must use localhost or a loopback IP; use an SSH tunnel for remote access")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("invalid listen port")
	}
	return nil
}
func acquireLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another instance is already using this data directory")
	}
	return f, nil
}

type targetsFlag []string

func (t *targetsFlag) String() string     { return fmt.Sprint([]string(*t)) }
func (t *targetsFlag) Set(v string) error { *t = append(*t, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "PingOne Promote:", err)
		os.Exit(1)
	}
}
func run() error {
	address := flag.String("listen", "127.0.0.1:8787", "loopback address for the browser interface")
	dataDir := flag.String("data-dir", "", "local configuration directory (defaults to OS user config directory)")
	noBrowser := flag.Bool("no-browser", false, "print startup URL without opening a browser")
	var localTargets targetsFlag
	flag.Var(&localTargets, "local-target", "allow direct deployment to this NON-PRODUCTION environment ID; repeat for additional non-production targets (default: GitLab-only)")
	flag.Parse()
	if flag.NArg() > 0 {
		return errors.New("unexpected positional arguments; use --help")
	}
	if err := validateListen(*address); err != nil {
		return err
	}
	if *dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*dataDir = filepath.Join(base, "pingone-promote")
	}
	lock, err := acquireLock(*dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	st, err := store.Open(*dataDir)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	token := app.RandomToken()
	handler, err := app.NewWithPolicy(st, token, app.Policy{LocalTargets: localTargets})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	link := "http://" + listener.Addr().String() + "/?token=" + token
	fmt.Printf("PingOne Promote is running locally.\nOpen: %s\nProfiles and history: %s\nPress Ctrl+C to stop.\n", link, *dataDir)
	if !*noBrowser {
		var command *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			command = exec.Command("open", link)
		case "linux":
			command = exec.Command("xdg-open", link)
		}
		if command != nil {
			if err := command.Start(); err == nil {
				go command.Wait()
			} else {
				fmt.Fprintln(os.Stderr, "Open the printed URL in your browser.")
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownComplete := make(chan struct{})
	go func() {
		defer close(shutdownComplete)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownComplete
	return nil
}
