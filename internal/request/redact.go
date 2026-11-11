package request

import (
	"regexp"
	"strings"

	"pingone-promote/internal/secretscan"
)

// Redaction is the last step of every run, whatever its status: a blocked
// or failed result can carry a diff, an error or a warning that quotes
// exported or requested content, and result.json is shown to people and
// stored. The secretscan heuristics decide what looks like a secret; the
// replacement never repeats the value.

const redacted = "[redacted]"

var (
	// pemBlock spans a private key from its header to its footer (or to the
	// end of the text when the footer is missing).
	pemBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)
	// jwtToken is secretscan.JWT plus the signature segment.
	jwtToken = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	// authHeader is a bearer or basic credential of any shape (opaque
	// tokens are not JWT-shaped), with an Authorization prefix if present.
	authHeader = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)?\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// quotedAttr is one `name = "value"` (HCL) or `"name": "value"` (JSON)
	// pair, optionally preceded by a diff marker and indentation.
	quotedAttr = regexp.MustCompile(`("?)([A-Za-z_][A-Za-z0-9_.-]*)("?\s*[=:]\s*)"((?:[^"\\\n]|\\.)*)"`)
)

// redactText removes private keys, JWTs and literal values under
// secret-looking names from free text or a diff.
func redactText(s string) string {
	if s == "" {
		return s
	}
	s = pemBlock.ReplaceAllString(s, redacted+" private key")
	s = jwtToken.ReplaceAllString(s, redacted+" token")
	s = authHeader.ReplaceAllString(s, "${1}${2} "+redacted)
	return quotedAttr.ReplaceAllStringFunc(s, func(m string) string {
		g := quotedAttr.FindStringSubmatch(m)
		value := strings.ReplaceAll(g[4], `\"`, `"`)
		if !secretscan.SuspectLiteral(g[2], value) {
			return m
		}
		return g[1] + g[2] + g[3] + `"` + redacted + `"`
	})
}

// redact applies redactText to every free-text field of the result.
func (r *Result) redact() {
	r.Diff = redactText(r.Diff)
	for i, w := range r.Warnings {
		r.Warnings[i] = redactText(w)
	}
	for env, bs := range r.Blockers {
		for i := range bs {
			bs[i].Message = redactText(bs[i].Message)
		}
		r.Blockers[env] = bs
	}
	for i := range r.Selected {
		r.Selected[i].Query = redactText(r.Selected[i].Query)
	}
	for i := range r.Changes {
		r.Changes[i].Reason = redactText(r.Changes[i].Reason)
	}
	if r.Error != nil {
		r.Error.Message = redactText(r.Error.Message)
		for i := range r.Error.Candidates {
			r.Error.Candidates[i].Query = redactText(r.Error.Candidates[i].Query)
		}
	}
}

// RedactText removes private keys, JWTs and literal values under
// secret-looking names from free text, for callers that log or show text
// outside a Result.
func RedactText(s string) string { return redactText(s) }
