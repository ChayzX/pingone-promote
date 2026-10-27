package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"pingone-promote/internal/pingone"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var safeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate profile ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validateProfile(p pingone.Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if _, ok := pingone.RegionDomains[p.Region]; !ok {
		return fmt.Errorf("unknown PingOne region %q", p.Region)
	}
	if !safeID.MatchString(p.ID) {
		return errors.New("profile ID must contain 1-128 letters, digits, underscores, or hyphens")
	}
	for name, value := range map[string]string{"environment ID": p.EnvironmentID, "client ID": p.ClientID} {
		if !safeID.MatchString(value) {
			return fmt.Errorf("%s must contain 1-128 letters, digits, underscores, or hyphens", name)
		}
	}
	if p.AuthEnvironmentID != "" && !safeID.MatchString(p.AuthEnvironmentID) {
		return errors.New("authentication environment ID must contain 1-128 letters, digits, underscores, or hyphens")
	}
	switch p.SecretMode {
	case "session", "keychain":
		if p.SecretEnv != "" {
			return errors.New("secret environment variable is only valid in env mode")
		}
	case "env":
		if !safeEnvName.MatchString(p.SecretEnv) {
			return errors.New("a valid secret environment variable name is required in env mode")
		}
	default:
		return fmt.Errorf("secret mode must be session, env, or keychain")
	}
	return nil
}
