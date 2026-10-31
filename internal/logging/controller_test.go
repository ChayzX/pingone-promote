package logging

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"
)

func TestControllerDebugLevelFiltersBoundedEvents(t *testing.T) {
	var output lockedBuffer
	controller, err := NewController(Config{Level: "info", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	controller.Event(context.Background(), slog.LevelDebug, "http.dispatch", "http", "api.session", "GET", "0011223344556677", "", 0, 0)
	if output.String() != "" {
		t.Fatalf("debug event escaped info filter: %s", output.String())
	}
	settings := controller.Settings()
	settings.Debug, settings.Level = true, "debug"
	if err := controller.Configure(settings); err != nil {
		t.Fatal(err)
	}
	controller.Event(context.Background(), slog.LevelDebug, "http.dispatch", "http", "api.session", "GET", "0011223344556678", "", 0, 0)
	if !strings.Contains(output.String(), `"event":"http.dispatch"`) {
		t.Fatalf("debug event not emitted after enabling debug: %s", output.String())
	}
}

func TestControllerCanonicalizesInitialSettings(t *testing.T) {
	controller, err := NewController(Config{Level: "DEBUG", Format: "TEXT"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	settings := controller.Settings()
	if settings.Level != "debug" || !settings.Debug || settings.Format != "text" {
		t.Fatalf("initial settings were not canonicalized: %+v", settings)
	}
}

func TestControllerFailedSinkChangeRetainsSettingsAndOutput(t *testing.T) {
	var output lockedBuffer
	controller, err := NewController(Config{Level: "info", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	before := controller.Settings()
	failed := before
	failed.Destination, failed.File = "file", "relative/path.log"
	if err := controller.Configure(failed); err == nil {
		t.Fatal("expected relative file path rejection")
	}
	if got := controller.Settings(); got != before {
		t.Fatalf("settings changed after failed sink open: before=%+v after=%+v", before, got)
	}
	directory, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failed.File = directory
	if err := controller.Configure(failed); err == nil {
		t.Fatal("expected non-file destination rejection")
	}
	if got := controller.Settings(); got != before {
		t.Fatalf("settings changed after failed file open: before=%+v after=%+v", before, got)
	}
	controller.Event(context.Background(), slog.LevelInfo, "http.request", "http", "api.session", "GET", "0011223344556677", "", 200, time.Millisecond)
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("old sink stopped receiving events after failed change: %s", output.String())
	}
}

func TestControllerSuccessfulFileSwitchAndConcurrentReconfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file output is unavailable without a native private ACL")
	}
	var output lockedBuffer
	controller, err := NewController(Config{Level: "info", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	controller.Event(context.Background(), slog.LevelInfo, "cli.startup", "startup", "", "", "", "", 0, 0)
	settings := controller.Settings()
	settings.Destination, settings.File = "file", path
	if err := controller.Configure(settings); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("old sink did not retain its pre-switch event: %s", output.String())
	}
	const workers, each = 4, 30
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				controller.Event(context.Background(), slog.LevelInfo, "http.request", "http", "api.session", "GET", "0011223344556677", "", 200, time.Millisecond)
			}
		}(worker)
	}
	for i := 0; i < 10; i++ {
		current := controller.Settings()
		if current.Format == "json" {
			current.Format = "text"
		} else {
			current.Format = "json"
		}
		if err := controller.Configure(current); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != workers*each {
		t.Fatalf("logged %d events, want %d", lines, workers*each)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("log mode is not private: info=%v err=%v", info, err)
	}
}
