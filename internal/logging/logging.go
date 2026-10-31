// Package logging provides the application's bounded, structured log surface.
// Callers provide only fixed event, stage, and route names; arbitrary errors,
// paths, request data, and identifiers are intentionally not accepted.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Level  string
	Format string
	File   string
}

type Logger struct{ inner *slog.Logger }

// RuntimeSettings are the effective per-process settings exposed to the
// authenticated local UI. A file path is returned only to that UI caller.
type RuntimeSettings struct {
	Level                 string `json:"level"`
	Debug                 bool   `json:"debug"`
	Format                string `json:"format"`
	Destination           string `json:"destination"`
	File                  string `json:"file"`
	FileSupported         bool   `json:"fileSupported"`
	FileUnavailableReason string `json:"fileUnavailableReason,omitempty"`
	Warning               string `json:"warning,omitempty"`
}

// Controller changes the active sink only after a replacement is ready. The
// RW lock lets settings changes wait for in-flight writes before closing the
// old sink, while readers never observe a partially updated logger.
type Controller struct {
	mu             sync.RWMutex
	configureMu    sync.Mutex
	logger         *Logger
	closeSink      func() error
	config         Config
	stderr         io.Writer
	cleanupWarning bool
}

type EventLogger interface {
	Event(context.Context, slog.Level, string, string, string, string, string, string, int, time.Duration)
}

func NewController(cfg Config, stderr io.Writer) (*Controller, error) {
	cfg.Level = strings.ToLower(cfg.Level)
	cfg.Format = strings.ToLower(cfg.Format)
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.Level == "warning" {
		cfg.Level = "warn"
	}
	if cfg.Format == "" {
		cfg.Format = "json"
	}
	if cfg.File != "" {
		absolute, err := filepath.Abs(cfg.File)
		if err != nil {
			return nil, errors.New("log file path must be absolute")
		}
		cfg.File = absolute
	}
	logger, closeSink, err := Open(cfg, stderr)
	if err != nil {
		return nil, err
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return &Controller{logger: logger, closeSink: closeSink, config: cfg, stderr: stderr}, nil
}

func FileLoggingSupport() (bool, string) {
	if runtime.GOOS == "windows" {
		return false, "File output is unavailable on Windows because the app cannot set a private file ACL. Choose stderr or redirect stderr through an ACL-controlled path."
	}
	return true, ""
}

func (c *Controller) Settings() RuntimeSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	settings := RuntimeSettings{Level: c.config.Level, Debug: c.config.Level == "debug", Format: c.config.Format, Destination: "stderr"}
	if c.config.File != "" {
		settings.Destination, settings.File = "file", c.config.File
	}
	settings.FileSupported, settings.FileUnavailableReason = FileLoggingSupport()
	if c.cleanupWarning {
		settings.Warning = "The previous log destination did not close cleanly; current settings are active."
	}
	return settings
}

func (c *Controller) Configure(settings RuntimeSettings) error {
	c.configureMu.Lock()
	defer c.configureMu.Unlock()
	if settings.Debug {
		if settings.Level != "debug" {
			return errors.New("debug mode requires the debug log level")
		}
	} else if settings.Level != "info" && settings.Level != "warn" && settings.Level != "error" {
		return errors.New("minimum level must be info, warn, or error when debug is off")
	}
	if settings.Format != "json" && settings.Format != "text" {
		return errors.New("format must be json or text")
	}
	cfg := Config{Level: settings.Level, Format: settings.Format}
	switch settings.Destination {
	case "stderr":
		if settings.File != "" {
			return errors.New("clear the file path when stderr is selected")
		}
	case "file":
		if supported, reason := FileLoggingSupport(); !supported {
			return errors.New(reason)
		}
		if settings.File == "" || len(settings.File) > 4096 || strings.TrimSpace(settings.File) != settings.File || strings.ContainsAny(settings.File, "\r\n\x00") || !filepath.IsAbs(settings.File) {
			return errors.New("file destination requires an absolute path without newlines")
		}
		cfg.File = settings.File
	default:
		return errors.New("destination must be stderr or file")
	}
	newLogger, newClose, err := Open(cfg, c.stderr)
	if err != nil {
		return errors.New("could not open the requested destination; choose a writable regular file and retry. Current settings are unchanged.")
	}
	c.mu.Lock()
	oldClose := c.closeSink
	c.logger, c.closeSink, c.config = newLogger, newClose, cfg
	c.cleanupWarning = false
	c.mu.Unlock()
	if oldClose != nil {
		if err := oldClose(); err != nil {
			c.mu.Lock()
			c.cleanupWarning = true
			c.mu.Unlock()
		}
	}
	return nil
}

func (c *Controller) Event(ctx context.Context, level slog.Level, event, stage, route, method, requestID, errorClass string, status int, duration time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.logger != nil {
		c.logger.Event(ctx, level, event, stage, route, method, requestID, errorClass, status, duration)
	}
}

func (c *Controller) Close() error {
	c.configureMu.Lock()
	defer c.configureMu.Unlock()
	c.mu.Lock()
	closeSink := c.closeSink
	c.logger, c.closeSink = nil, nil
	c.mu.Unlock()
	if closeSink != nil {
		return closeSink()
	}
	return nil
}

type fallbackWriter struct{ primary, fallback io.Writer }

func (w fallbackWriter) Write(p []byte) (int, error) {
	n, err := w.primary.Write(p)
	if err == nil && n == len(p) {
		return n, nil
	}
	if w.fallback != nil {
		_, _ = fmt.Fprintln(w.fallback, "pingone-promote: structured log file write failed; copying event to stderr")
		if fallbackN, fallbackErr := w.fallback.Write(p); fallbackErr == nil && fallbackN == len(p) {
			return len(p), nil
		}
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

// Open creates a JSON or text logger. File output is append-only and private
// to the current user; a failure to open or secure it is returned to the CLI.
func Open(cfg Config, stderr io.Writer) (*Logger, func() error, error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	format := strings.ToLower(cfg.Format)
	if format != "" && format != "json" && format != "text" {
		return nil, nil, errors.New("invalid log format; use json or text")
	}
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "", "info":
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, nil, errors.New("invalid log level; use debug, info, warn, or error")
	}
	var dst io.Writer = stderr
	var file *os.File
	if cfg.File != "" {
		if runtime.GOOS == "windows" {
			return nil, nil, errors.New("file logging is unavailable on Windows; use stderr or configure a private ACL outside the application")
		}
		if info, err := os.Lstat(cfg.File); err == nil {
			if !info.Mode().IsRegular() {
				return nil, nil, errors.New("log destination must be a regular file")
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, errors.New("could not inspect log destination")
		}
		dir := filepath.Dir(cfg.File)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, nil, errors.New("could not create log directory")
		}
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, errors.New("could not open log file")
		}
		openedInfo, statErr := f.Stat()
		pathInfo, pathErr := os.Lstat(cfg.File)
		if statErr != nil || pathErr != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			f.Close()
			return nil, nil, errors.New("log destination changed while it was opened")
		}
		if err := f.Chmod(0600); err != nil {
			f.Close()
			return nil, nil, errors.New("could not secure log file permissions")
		}
		file, dst = f, fallbackWriter{primary: f, fallback: stderr}
	}
	var handler slog.Handler
	options := &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == "time" {
			a.Value = slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano))
		}
		return a
	}}
	switch format {
	case "", "json":
		handler = slog.NewJSONHandler(dst, options)
	case "text":
		handler = slog.NewTextHandler(dst, options)
	}
	close := func() error {
		if file != nil {
			return file.Close()
		}
		return nil
	}
	return &Logger{inner: slog.New(handler)}, close, nil
}

// Event emits a fixed event with only known-safe scalar fields. Empty fields
// are omitted. Do not add untrusted or tenant-derived values to this API.
func (l *Logger) Event(ctx context.Context, level slog.Level, event, stage, route, method, requestID, errorClass string, status int, duration time.Duration) {
	if l == nil || l.inner == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attrs := []any{slog.String("event", safeName(event, "event")), slog.String("stage", safeName(stage, "unknown"))}
	if route != "" {
		attrs = append(attrs, slog.String("route", safeRoute(route)))
	}
	if method != "" {
		attrs = append(attrs, slog.String("method", safeMethod(method)))
	}
	if safeRequestID(requestID) {
		attrs = append(attrs, slog.String("request_id", requestID))
	}
	if errorClass != "" {
		attrs = append(attrs, slog.String("error_class", safeName(errorClass, "operation_failed")))
	}
	if status > 0 {
		attrs = append(attrs, slog.Int("http_status", status))
	}
	if duration > 0 {
		attrs = append(attrs, slog.Int64("duration_ms", duration.Milliseconds()))
	}
	l.inner.Log(ctx, level, "application event", attrs...)
}

func safeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect:
		return method
	default:
		return "OTHER"
	}
}

func safeRequestID(id string) bool {
	if len(id) == 16 {
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(id, "local-") && len(id) > 6 {
		for _, r := range id[6:] {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	return false
}

func safeName(s, fallback string) string {
	allowed := map[string]bool{
		"http.request": true, "app.failure": true, "cli.startup": true, "cli.failure": true, "cli.shutdown": true,
		"http.dispatch": true,
		"startup":       true, "configuration": true, "storage": true, "listener": true, "http_server": true, "http": true, "application": true, "operation": true, "command": true,
		"startup_failed": true, "request_failed": true, "request_rejected": true, "server_failed": true, "command_failed": true,
		"operation_failed": true,
	}
	if allowed[s] {
		return s
	}
	return fallback
}

func safeRoute(s string) string {
	switch s {
	case "bootstrap", "static", "api.session", "api.profiles", "api.profile", "api.history", "api.inventory", "api.mappings", "api.plans", "api.gitlab", "api.request", "api.other", "not_found":
		return s
	default:
		return "api.other"
	}
}

var requestCounter atomic.Uint64

func requestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// The process-local counter preserves uniqueness if the system random
	// source fails. It is correlation metadata, not an authentication token.
	return fmt.Sprintf("local-%x", requestCounter.Add(1))
}

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HTTP adds per-request correlation and logs only an allowlisted route name,
// method, status, and duration. It never records URL, query, host, or headers.
func HTTP(logger EventLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		sw := &statusWriter{ResponseWriter: w}
		started := time.Now()
		if logger != nil {
			logger.Event(ctx, slog.LevelDebug, "http.dispatch", "http", routeName(r), r.Method, id, "", 0, 0)
		}
		next.ServeHTTP(sw, r.WithContext(ctx))
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		level, class := slog.LevelInfo, ""
		if status >= 500 {
			level, class = slog.LevelError, "request_failed"
		} else if status >= 400 {
			level, class = slog.LevelWarn, "request_rejected"
		}
		if logger != nil {
			logger.Event(ctx, level, "http.request", "http", routeName(r), r.Method, id, class, status, time.Since(started))
		}
	})
}

func routeName(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/" && r.URL.Query().Has("token"):
		return "bootstrap"
	case p == "/" || p == "/app.js" || p == "/style.css":
		return "static"
	case p == "/api/session":
		return "api.session"
	case p == "/api/profiles":
		return "api.profiles"
	case strings.HasPrefix(p, "/api/profiles/"):
		return "api.profile"
	case p == "/api/history":
		return "api.history"
	case p == "/api/inventory":
		return "api.inventory"
	case p == "/api/mappings":
		return "api.mappings"
	case p == "/api/plans" || strings.HasPrefix(p, "/api/plans/"):
		return "api.plans"
	case p == "/api/gitlab" || p == "/api/gitlab/submit":
		return "api.gitlab"
	case p == "/api/requests/preview":
		return "api.request"
	case strings.HasPrefix(p, "/api/"):
		return "api.other"
	default:
		return "not_found"
	}
}
