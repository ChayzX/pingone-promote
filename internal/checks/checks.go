// Package checks holds automated acceptance checks: offline plan assertions,
// keyed evidence capture/compare and read-only PingOne capability probes.
// Nothing here writes configuration; evidence capture creates a native
// snapshot exactly as planning already does.
package checks

import (
	"fmt"
	"io"
	"strings"
)

// Status is the outcome of one check.
type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Check is one named assertion and what was observed.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Report is an ordered list of checks.
type Report struct {
	Title  string  `json:"title"`
	Checks []Check `json:"checks"`
}

func (r *Report) add(status Status, name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
}

// Pass, Warn and Fail append a check.
func (r *Report) Pass(name, format string, args ...any) { r.add(Pass, name, format, args...) }
func (r *Report) Warn(name, format string, args ...any) { r.add(Warn, name, format, args...) }
func (r *Report) Fail(name, format string, args ...any) { r.add(Fail, name, format, args...) }

// Merge appends another report's checks.
func (r *Report) Merge(o Report) { r.Checks = append(r.Checks, o.Checks...) }

// Failed reports whether any check failed.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Counts returns pass, warn and fail totals.
func (r Report) Counts() (p, w, f int) {
	for _, c := range r.Checks {
		switch c.Status {
		case Pass:
			p++
		case Warn:
			w++
		case Fail:
			f++
		}
	}
	return
}

// WriteText prints an aligned plain-text report suitable for CI logs.
func (r Report) WriteText(w io.Writer) error {
	p, wn, f := r.Counts()
	if _, err := fmt.Fprintf(w, "%s: %d passed, %d warnings, %d failed\n", r.Title, p, wn, f); err != nil {
		return err
	}
	for _, c := range r.Checks {
		if _, err := fmt.Fprintf(w, "  [%s] %s: %s\n", c.Status, c.Name, oneLine(c.Detail)); err != nil {
			return err
		}
	}
	return nil
}

// Markdown renders the report as a table for reviewers.
func (r Report) Markdown() string {
	var b strings.Builder
	p, w, f := r.Counts()
	fmt.Fprintf(&b, "## %s\n\n%d passed, %d warnings, %d failed.\n\n| Result | Check | Detail |\n| --- | --- | --- |\n", r.Title, p, w, f)
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Status, cell(c.Name), cell(c.Detail))
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(oneLine(s), "|", "\\|"), "`", "'")
}
