package server

import (
	"context"
	"errors"
	"log/slog"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/request"
)

// Plain-language export failures. The exporter's own text (a tool's stderr,
// an API response) never reaches a page: it can quote tenant content or a
// credential. It is logged, redacted, at debug level only.
const (
	msgExportTool     = "The export tool failed while reading the source environment. Check that the worker application can read it, then try again. The operator can find details in the service log."
	msgExportPin      = "The export tool on the server does not match its pinned version or checksum. The operator must reinstall the pinned tools."
	msgLabelCollision = "Two resources in the source environment would get the same Terraform name, so nothing was exported. Rename one of them in the source environment (for example a flow-scoped variable that shares a name with another), then try again."
	msgExportOther    = "Reading the source environment failed. Try again; if it keeps failing, the operator can find details in the service log."
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
	}
	p.log.Debug("export failed", "op", op, "detail", p.redact(request.RedactText(err.Error())))
	return &exportError{msg: msg, err: err}
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
