package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/request"
)

// Plain-language export failures. The exporter's own text (a tool's stderr,
// an API response) never reaches a page: it can quote tenant content or a
// credential. It is logged, redacted, at debug level only.
const (
	msgExportTool     = "The DaVinci export tool failed while reading the source environment. Check that the worker application can read it, then try again. The operator can find the tool's output in the service log (podman logs pingone-promote, or journalctl -u pingone-promote)."
	msgExportPin      = "The export tool on the server does not match its pinned version or checksum. The operator must reinstall the pinned tools."
	msgLabelCollision = "Two resources in the source environment would get the same Terraform name, so nothing was exported. Rename one of them in the source environment (for example a flow-scoped variable that shares a name with another), then try again."
	msgExportOther    = "Reading the source environment failed. Try again; if it keeps failing, the operator can find details in the service log (podman logs pingone-promote, or journalctl -u pingone-promote)."
)

// exportError carries a fixed message and keeps the original error for
// errors.Is/As (the engine classifies a label collision as a blocker).
type exportError struct {
	msg string
	err error
}

func (e *exportError) Error() string { return e.msg }
func (e *exportError) Unwrap() error { return e.err }

// plainErrors wraps the exporter the server was given.
type plainErrors struct {
	inner  request.Exporter
	log    *slog.Logger
	redact func(string) string
}

func (p plainErrors) wrap(op string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ex *exporter.ExecError
	var lc *exporter.LabelCollisionError
	msg := msgExportOther
	switch {
	case errors.As(err, &lc):
		msg = msgLabelCollision
	case errors.Is(err, exporter.ErrChecksum), errors.Is(err, exporter.ErrVersion):
		msg = msgExportPin
	case errors.As(err, &ex):
		msg = msgExportTool
	default:
		if m, ok := pingoneMessage(err); ok {
			msg = m
		}
	}
	detail := p.redact(request.RedactText(err.Error()))
	if msg != msgExportOther && msg != msgExportTool {
		// This tool's own wording (PingOne client, pins, labels).
		p.log.Warn("export failed", "op", op, "detail", detail)
	} else {
		// Other text (a tool's output, ...) can quote tenant content: its
		// detail is logged at debug level only.
		p.log.Warn("export failed", "op", op, "cause", "not shown at this log level; restart the server with --debug to log it")
		p.log.Debug("export failed", "op", op, "detail", detail)
	}
	return &exportError{msg: msg, err: err}
}

// pingoneMessage explains a failure of the PingOne API client. Its error
// texts are this tool's own (an API path, an HTTP status, a network
// cause), never a response body, so they can be shown.
func pingoneMessage(err error) (string, bool) {
	var ae *pingone.APIError
	if errors.As(err, &ae) {
		var hint string
		switch {
		case ae.Status == http.StatusUnauthorized:
			hint = " PingOne refused the worker's token for the source environment. Give the worker application a read role (for example Environment Admin or Identity Data Read Only) on the source environment itself, not only on the environment it lives in; a new role takes effect on the next token, so wait a minute and try again."
		case ae.Status == http.StatusForbidden:
			hint = " The worker application lacks a read role for this on the source environment."
		case ae.Status == http.StatusTooManyRequests:
			hint = " PingOne is throttling requests; try again in a few minutes."
		case ae.Status >= 500:
			hint = " PingOne had a server error; try again in a few minutes."
		}
		return fmt.Sprintf("PingOne answered HTTP %d to GET %s.%s", ae.Status, ae.Path, hint), true
	}
	s := err.Error()
	if i := strings.Index(s, "pingone: "); i >= 0 && (i == 0 || strings.HasPrefix(s, "coreexport: ") || strings.HasPrefix(s, "multiexport: ")) {
		return "PingOne: " + s[i+len("pingone: "):], true
	}
	return "", false
}

// refresh reads the source's item list again, through the inner
// exporter's Refresh when it keeps a cached read (which stays in use until
// the new one is in), else through CatalogAllIDs.
func (p plainErrors) refresh(ctx context.Context, c exporter.Credentials) ([]exporter.Resource, error) {
	if r, ok := p.inner.(interface {
		Refresh(context.Context, exporter.Credentials) ([]exporter.Resource, error)
	}); ok {
		out, err := r.Refresh(ctx, c)
		if err != nil {
			return nil, p.wrap("catalog", err)
		}
		return out, nil
	}
	return p.CatalogAllIDs(ctx, c)
}

// davinciProblem explains why the latest catalog read left out DaVinci
// items, in plain words ("" when nothing is missing or the exporter
// cannot say).
func (p plainErrors) davinciProblem(c exporter.Credentials) string {
	dp, ok := p.inner.(interface {
		DaVinciProblem(exporter.Credentials) error
	})
	if !ok {
		return ""
	}
	err := dp.DaVinciProblem(c)
	if err == nil {
		return ""
	}
	return p.wrap("davinci catalog", err).Error()
}

func (p plainErrors) Catalog(ctx context.Context, c exporter.Credentials, withIDs bool) ([]exporter.Resource, error) {
	out, err := p.inner.Catalog(ctx, c, withIDs)
	return out, p.wrap("catalog", err)
}

func (p plainErrors) CatalogAllIDs(ctx context.Context, c exporter.Credentials) ([]exporter.Resource, error) {
	out, err := p.inner.CatalogAllIDs(ctx, c)
	return out, p.wrap("catalog", err)
}

func (p plainErrors) ScopedExport(ctx context.Context, c exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	out, err := p.inner.ScopedExport(ctx, c, seeds, exclude)
	return out, p.wrap("export", err)
}

func (p plainErrors) WholeExport(ctx context.Context, c exporter.Credentials, exclude []string) (*exporter.Export, error) {
	out, err := p.inner.WholeExport(ctx, c, exclude)
	return out, p.wrap("export", err)
}

func (p plainErrors) SelectedExport(ctx context.Context, c exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	sel, ok := p.inner.(request.SelectedExporter)
	if !ok {
		return nil, errors.New("this exporter cannot export without upstream dependencies")
	}
	out, err := sel.SelectedExport(ctx, c, seeds, exclude)
	return out, p.wrap("export", err)
}

// scrub removes the exact loaded secret values from every free-text field
// of a result, in addition to the engine's heuristic redaction.
func (s *Server) scrub(res *request.Result) {
	if res == nil {
		return
	}
	res.Diff = s.redact(res.Diff)
	for i := range res.Warnings {
		res.Warnings[i] = s.redact(res.Warnings[i])
	}
	for env, bs := range res.Blockers {
		for i := range bs {
			bs[i].Message = s.redact(bs[i].Message)
		}
		res.Blockers[env] = bs
	}
	for i := range res.Changes {
		res.Changes[i].Reason = s.redact(res.Changes[i].Reason)
	}
	for i := range res.Selected {
		res.Selected[i].Query = s.redact(res.Selected[i].Query)
	}
	for name, v := range res.Variables {
		v.SourceValue, v.Description = s.redact(v.SourceValue), s.redact(v.Description)
		res.Variables[name] = v
	}
	if res.Error != nil {
		res.Error.Message = s.redact(res.Error.Message)
		for i := range res.Error.Candidates {
			res.Error.Candidates[i].Query = s.redact(res.Error.Candidates[i].Query)
		}
	}
}
