// PingOne Promote is a local, explicitly scoped configuration-promotion UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	"pingone-promote/internal/buildinfo"
	"pingone-promote/internal/logging"
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
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, errors.New("another instance is already using this data directory")
	}
	return f, nil
}

type targetsFlag []string

func (t *targetsFlag) String() string     { return fmt.Sprint([]string(*t)) }
func (t *targetsFlag) Set(v string) error { *t = append(*t, v); return nil }

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "PingOne Promote:", err)
		os.Exit(1)
	}
}
func run() (runErr error) {
	version := flag.Bool("version", false, "print version and exit")
	address := flag.String("listen", "127.0.0.1:8787", "loopback address for the browser interface")
	dataDir := flag.String("data-dir", "", "local configuration directory (defaults to OS user config directory)")
	noBrowser := flag.Bool("no-browser", false, "print startup URL without opening a browser")
	logLevel := flag.String("log-level", envOr("PINGONE_PROMOTE_LOG_LEVEL", "info"), "structured log level: debug, info, warn, or error")
	logFormat := flag.String("log-format", envOr("PINGONE_PROMOTE_LOG_FORMAT", "json"), "structured log format: json or text")
	logFile := flag.String("log-file", os.Getenv("PINGONE_PROMOTE_LOG_FILE"), "append structured logs to this private file instead of stderr")
	var localTargets targetsFlag
	flag.Var(&localTargets, "local-target", "allow direct deployment to this NON-PRODUCTION environment ID; repeat for additional non-production targets (default: GitLab-only)")
	flag.Parse()
	if *version {
		fmt.Printf("pingone-promote %s\n", buildinfo.Version)
		return nil
	}
	stage := "startup"
	logger, err := logging.NewController(logging.Config{Level: *logLevel, Format: *logFormat, File: *logFile}, os.Stderr)
	if err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			failureClass := "startup_failed"
			if stage == "http_server" {
				failureClass = "server_failed"
			}
			logger.Event(context.Background(), slog.LevelError, "cli.failure", stage, "", "", "", failureClass, 0, 0)
		}
		if err := logger.Close(); err != nil && runErr == nil {
			runErr = errors.New("could not close structured log output")
		}
	}()
	if flag.NArg() > 0 {
		return errors.New("unexpected positional arguments; use --help")
	}
	if err := validateListen(*address); err != nil {
		return err
	}
	if *dataDir == "" {
		stage = "configuration"
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
	stage = "storage"
	st, err := store.Open(*dataDir)
	if err != nil {
		return err
	}
	stage = "listener"
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	token := app.RandomToken()
	handler, err := app.NewWithPolicyAndLogger(st, token, app.Policy{LocalTargets: localTargets}, logger)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: logging.HTTP(logger, handler), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	link := "http://" + listener.Addr().String() + "/?token=" + token
	fmt.Printf("PingOne Promote is running locally.\nOpen: %s\nProfiles and history: %s\nPress Ctrl+C to stop.\n", link, *dataDir)
	logger.Event(context.Background(), 0, "cli.startup", "http_server", "", "", "", "", 0, 0)
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
	stage = "http_server"
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownComplete
	logger.Event(context.Background(), 0, "cli.shutdown", "http_server", "", "", "", "", 0, 0)
	return nil
}
