package app

import (
	"errors"
	"pingone-promote/internal/logging"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/store"
	"regexp"
	"sort"
)

// Policy is startup configuration, not a browser-editable profile property.
// Unlisted destinations use GitLab. Only explicitly designated non-production
// environment IDs may be added to LocalTargets by the operator.
type Policy struct{ LocalTargets []string }

func NewWithPolicy(st *store.Store, token string, policy Policy) (*Server, error) {
	return NewWithPolicyAndLogger(st, token, policy, nil)
}

// NewWithPolicyAndLogger wires structured, redacted application failures to
// the caller's logger while preserving request correlation from HTTP context.
func NewWithPolicyAndLogger(st *store.Store, token string, policy Policy, logger *logging.Controller) (*Server, error) {
	s := New(st, token)
	s.logger = logger
	safe := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	for _, id := range policy.LocalTargets {
		if !safe.MatchString(id) {
			return nil, errors.New("invalid non-production local target environment ID")
		}
		s.localTargets[id] = true
	}
	return s, nil
}
func (s *Server) localAllowed(p pingone.Profile) bool { return s.localTargets[p.EnvironmentID] }
func (s *Server) localTargetIDs() []string {
	ids := []string{}
	for id := range s.localTargets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

const gitlabOnlyMessage = "This destination is GitLab-only. Submit a promotion request; deployment and rollback execute in an approved GitLab pipeline."
