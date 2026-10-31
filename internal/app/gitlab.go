package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/operation"
)

func defaultGitLab(c gitlab.Config) gitlab.Config {
	if c.TokenEnv == "" {
		c.TokenEnv = "GITLAB_TOKEN"
	}
	if c.Environment == "" {
		c.Environment = "production"
	}
	return c
}
func configBinding(c gitlab.Config) string {
	return strings.TrimRight(c.BaseURL, "/") + "/" + c.ProjectID
}
func (s *Server) gitlabToken(c gitlab.Config) string {
	if s.gitlabTokenBinding == configBinding(c) && strings.TrimSpace(s.gitlabSessionToken) != "" {
		return strings.TrimSpace(s.gitlabSessionToken)
	}
	return strings.TrimSpace(os.Getenv(c.TokenEnv))
}
func missingGitLabToken(c gitlab.Config) error {
	return operation.NewFailure("gitlab_token_required", "configuration", fmt.Sprintf("GitLab API token is required: enter a project access token for this app session, or set %s in the app process environment before launch", c.TokenEnv), "configure_gitlab_token", "", nil)
}
func (s *Server) gitlabRoute(w http.ResponseWriter, r *http.Request) error {
	switch {
	case r.URL.Path == "/api/gitlab" && r.Method == "GET":
		c, err := gitlab.LoadConfig(s.store.Directory())
		if err != nil {
			return err
		}
		c = defaultGitLab(c)
		writeJSON(w, map[string]any{"config": c, "configured": c.BaseURL != "" && c.Validate() == nil, "tokenAvailable": s.gitlabToken(c) != ""})
		return nil
	case r.URL.Path == "/api/gitlab" && r.Method == "POST":
		var input struct {
			gitlab.Config
			Token string `json:"token"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		c := defaultGitLab(input.Config)
		if err := c.Validate(); err != nil {
			return knownValidationFailure(err.Error())
		}
		sessionToken := strings.TrimSpace(input.Token)
		token := sessionToken
		if token == "" {
			token = s.gitlabToken(c)
		}
		if token == "" {
			return missingGitLabToken(c)
		}
		client, err := s.gitlabFactory(c, token)
		if err != nil {
			return err
		}
		project, err := client.VerifyProject(r.Context())
		if err != nil {
			return problem(502, err)
		}
		if err = gitlab.SaveConfig(s.store.Directory(), c); err != nil {
			return err
		}
		if sessionToken != "" {
			s.gitlabSessionToken = sessionToken
			s.gitlabTokenBinding = configBinding(c)
		}
		writeJSON(w, map[string]any{"config": c, "configured": true, "tokenAvailable": true, "project": project})
		return nil
	case r.URL.Path == "/api/requests/preview" && r.Method == "POST":
		var input struct {
			inventoryPair
			Selected    []string          `json:"selected"`
			Mappings    map[string]string `json:"mappings"`
			Description string            `json:"description"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		a, b, err := s.pair(input.inventoryPair)
		if err != nil {
			return err
		}
		sp, err := s.profile(a.ProfileID)
		if err != nil {
			return err
		}
		tp, err := s.profile(b.ProfileID)
		if err != nil {
			return err
		}
		if sp.EnvironmentID != a.EnvironmentID || tp.EnvironmentID != b.EnvironmentID {
			return knownValidationFailure("profile environment identity changed; reload resources")
		}
		req, err := gitops.NewRequest(a.Inventory, b.Inventory, sp.Region, tp.Region, input.Selected, input.Mappings, input.Description)
		if err != nil {
			return knownValidationFailure(invalidPromotionInputMessage)
		}
		if _, err = gitops.Encode(req); err != nil {
			return knownValidationFailure("Promotion request could not be validated. Reload resources and review the selected items and mappings, then retry.")
		}
		s.requests[req.ID] = req
		writeJSON(w, map[string]any{"request": req, "issues": []string{}, "gitlabRequired": !s.localAllowed(tp)})
		return nil
	case r.URL.Path == "/api/gitlab/submit" && r.Method == "POST":
		var input struct {
			RequestID     string `json:"requestId"`
			BranchName    string `json:"branchName"`
			CommitMessage string `json:"commitMessage"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		req, ok := s.requests[input.RequestID]
		if !ok {
			return knownValidationFailure("request preview expired or unknown; build and review it before submission")
		}
		submissionOptions := gitlab.SubmissionOptions{BranchName: input.BranchName, CommitMessage: input.CommitMessage}
		if err := gitlab.ValidateSubmissionOptions(submissionOptions); err != nil {
			return knownValidationFailure(err.Error())
		}
		if s.submissionAttempts[req.ID] {
			return problem(409, knownValidationFailure("a submission was already attempted; inspect the GitLab branch before creating another request"))
		}
		config, err := gitlab.LoadConfig(s.store.Directory())
		if err != nil {
			return err
		}
		config = defaultGitLab(config)
		token := s.gitlabToken(config)
		if token == "" {
			return missingGitLabToken(config)
		}
		client, err := s.gitlabFactory(config, token)
		if err != nil {
			return err
		}
		raw, err := gitops.Encode(req)
		if err != nil {
			return knownValidationFailure("Saved promotion request could not be validated. Rebuild the preview before submitting it.")
		}
		if err = gitops.Validate(req); err != nil {
			return knownValidationFailure("Saved promotion request could not be validated. Rebuild the preview before submitting it.")
		}
		s.submissionAttempts[req.ID] = true
		title := "Promote selected PingOne resources"
		if req.Description != "" {
			title = req.Description
		}
		if len(title) > 180 {
			title = title[:180]
		}
		description := fmt.Sprintf("Promotion request `%s`\n\nSource environment: `%s` (snapshot `%s`)\nTarget environment: `%s`\nSelected resources: %d\nGitLab environment: `%s`\n\nThis merge request submits configuration for review. Actual deployment and rollback must execute through protected GitLab jobs. Review the configuration exports and final native plan before approving deployment.", req.ID, req.Source.ID, req.Source.SnapshotID, req.Target.ID, len(req.Selected), config.Environment)
		mr, err := client.SubmitWithOptions(r.Context(), req.ID, map[string]string{"request.json": string(raw) + "\n"}, title, description, submissionOptions)
		if err != nil {
			var preflightErr *gitlab.SubmissionPreflightError
			if errors.As(err, &preflightErr) {
				delete(s.submissionAttempts, req.ID)
				failure := operation.NewFailure("gitlab_pre_dispatch_failure", "preflight", "GitLab submission preflight failed; no GitLab writes occurred. Verify private project access and connection settings, then retry.", "retry_pre_dispatch", w.Header().Get("X-Request-ID"), err)
				replyNativeFailure(w, http.StatusBadGateway, failure, true)
				return nil
			}
			return problem(502, err)
		}
		writeJSON(w, map[string]any{"requestId": req.ID, "url": mr.WebURL, "branch": mr.Branch, "iid": mr.IID})
		return nil
	}
	return problem(404, knownValidationFailure("unknown GitLab endpoint"))
}
