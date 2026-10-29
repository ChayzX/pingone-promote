package gitlab

import "net/url"

// Test-only exports for the DaVinci cross-check (copied into a scratch clone).
func XValidate(baseURL, projectID, environment string) error {
	return Config{BaseURL: baseURL, ProjectID: projectID, Environment: environment}.withDefaults().Validate()
}

func XSameLocation(baseURL, raw string) bool {
	base, err := parseBase(baseURL)
	if err != nil {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return sameGitLabLocation(base, u)
}
