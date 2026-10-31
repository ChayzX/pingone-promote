package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"pingone-promote/internal/pingone"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var safeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidationError carries application-authored profile guidance that is safe
// to present to the local operator. Do not wrap storage or provider failures.
type ValidationError struct{ message string }

func (e *ValidationError) Error() string { return e.message }

func profileValidation(message string) error { return &ValidationError{message: message} }

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate profile ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validateProfile(p pingone.Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return profileValidation("profile name is required")
	}
	if _, ok := pingone.RegionDomains[p.Region]; !ok {
		return profileValidation("unknown PingOne region; choose a supported PingOne region")
	}
	if !safeID.MatchString(p.ID) {
		return profileValidation("profile ID must contain 1-128 letters, digits, underscores, or hyphens")
	}
	for name, value := range map[string]string{"environment ID": p.EnvironmentID, "client ID": p.ClientID} {
		if !safeID.MatchString(value) {
			return profileValidation(fmt.Sprintf("%s must contain 1-128 letters, digits, underscores, or hyphens", name))
		}
	}
	if p.AuthEnvironmentID != "" && !safeID.MatchString(p.AuthEnvironmentID) {
		return profileValidation("authentication environment ID must contain 1-128 letters, digits, underscores, or hyphens")
	}
	switch p.SecretMode {
	case "session", "keychain":
		if p.SecretEnv != "" {
			return profileValidation("secret environment variable is only valid in env mode")
		}
	case "env":
		if !safeEnvName.MatchString(p.SecretEnv) {
			return profileValidation("a valid secret environment variable name is required in env mode")
		}
	default:
		return profileValidation("secret mode must be session, env, or keychain")
	}
	return nil
}

// maxProfileNameRunes matches the maxlength of the profile form field.
const maxProfileNameRunes = 80

// validateProfileName is applied when saving only, so profiles already on disk
// remain usable.
func validateProfileName(name string) error {
	if strings.TrimSpace(name) == "" {
		return profileValidation("profile name is required")
	}
	if utf8.RuneCountInString(name) > maxProfileNameRunes {
		return profileValidation(fmt.Sprintf("profile name must be at most %d characters", maxProfileNameRunes))
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return profileValidation("profile name must not contain control characters")
		}
	}
	return nil
}
