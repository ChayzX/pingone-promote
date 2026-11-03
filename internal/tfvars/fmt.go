package tfvars

import (
	"context"

	"pingone-promote/internal/tffmt"
)

// The terraform fmt gate moved to internal/tffmt so the v2 path does not
// depend on this package; these names keep v1 callers working.
type (
	FormatterNotFoundError = tffmt.FormatterNotFoundError
	FormatterFailedError   = tffmt.FormatterFailedError
	FormatMismatchError    = tffmt.FormatMismatchError
)

// FormatTimeout bounds one formatter run.
const FormatTimeout = tffmt.FormatTimeout

// DefaultFormatters are tried in order when no binary path is configured.
var DefaultFormatters = tffmt.DefaultFormatters

// ResolveFormatter returns the absolute formatter path.
func ResolveFormatter(binPath string) (string, error) { return tffmt.ResolveFormatter(binPath) }

// FormatWithTerraform runs the fmt gate (see tffmt.FormatWithTerraform).
func FormatWithTerraform(ctx context.Context, binPath string, content []byte) ([]byte, error) {
	return tffmt.FormatWithTerraform(ctx, binPath, content)
}
