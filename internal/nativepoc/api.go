// Package nativepoc is a proof of concept: promotions through PingOne's
// native Configuration Management API, driven from a GitLab pipeline, with
// a field-level review of what each promotion changes in the target.
//
// The API shapes are the ones the earlier native client of this repository
// used against the live service (snapshots, promotions, promotionSteps,
// the execute and rollback media types). They are isolated in this file so
// a change in the API is a change here only.
package nativepoc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	executeType  = "application/vnd.pingidentity.promotion.execute+json"
	rollbackType = "application/vnd.pingidentity.promotion.rollback+json"
)

// Client calls the PingOne API as a worker application.
type Client struct {
	APIBase, AuthBase string // https://api.pingone.com, https://auth.pingone.com
	WorkerEnv         string
	ClientID, Secret  string
	HTTP              *http.Client
	Poll              time.Duration
	token             string
	expires           time.Time
}

// Ref names a resource or an environment.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Resource is one configuration resource of a snapshot.
type Resource struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	URL           string         `json:"url"`
	Type          string         `json:"type"`
	Configuration map[string]any `json:"configuration"`
	References    []string       `json:"references,omitempty"`
}

// Snapshot is an environment's configuration at one moment.
type Snapshot struct {
	ID        string
	Resources map[string]Resource // by resource ID
}

// Promotion is a native promotion (or the promotion a rollback creates).
type Promotion struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Source         Ref             `json:"sourceEnvironment"`
	Target         Ref             `json:"targetEnvironment"`
	SourceSnapshot Ref             `json:"sourceSnapshot"`
	TargetSnapshot Ref             `json:"targetSnapshot"`
	Errors         json.RawMessage `json:"errors,omitempty"`
}

// Step is one operation of a promotion plan.
type Step struct {
	Action  string          `json:"action"` // CREATE, UPDATE, DELETE
	URL     string          `json:"resourceUrl"`
	Source  Ref             `json:"sourceResource"`
	Target  Ref             `json:"targetResource"`
	Payload json.RawMessage `json:"payload"`
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.AuthBase+"/"+url.PathEscape(c.WorkerEnv)+"/as/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.ClientID, c.Secret)
	res, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("the worker token could not be requested: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || out.Token == "" {
		return "", fmt.Errorf("the worker token request returned HTTP %d; check the worker client ID, secret, environment and region", res.StatusCode)
	}
	c.token, c.expires = out.Token, time.Now().Add(time.Duration(out.Expires)*time.Second-30*time.Second)
	return c.token, nil
}

// do sends one request. path is "/v1/..." or an absolute URL under APIBase.
func (c *Client) do(ctx context.Context, method, path, contentType string, body, out any) error {
	u := path
	if strings.HasPrefix(path, "/") {
		u = c.APIBase + path
	} else if !strings.HasPrefix(path, c.APIBase+"/") {
		return errors.New("PingOne returned a link outside its API")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
		if contentType == "" {
			contentType = "application/json"
		}
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("PingOne could not be reached: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// The message PingOne gives, never the request (it may hold values).
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("PingOne returned HTTP %d for %s %s: %s %s", res.StatusCode, method, strings.TrimPrefix(u, c.APIBase), e.Code, e.Message)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// pages reads every page of a collection.
func (c *Client) pages(ctx context.Context, path, key string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for n := 0; path != ""; n++ {
		if n > 5000 {
			return nil, errors.New("PingOne paging did not end")
		}
		var page struct {
			Embedded map[string][]json.RawMessage `json:"_embedded"`
			Links    map[string]struct {
				Href string `json:"href"`
			} `json:"_links"`
		}
		if err := c.do(ctx, "GET", path, "", nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Embedded[key]...)
		path = page.Links["next"].Href
	}
	return all, nil
}

func envPath(env string) string { return "/v1/environments/" + url.PathEscape(env) }

func (c *Client) wait(ctx context.Context, what string, done func() (bool, error)) error {
	poll := c.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	for i := 0; i < 300; i++ {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return fmt.Errorf("%s did not finish in time", what)
}

// TakeSnapshot snapshots an environment and reads every resource in it.
func (c *Client) TakeSnapshot(ctx context.Context, env string) (*Snapshot, error) {
	var snap struct {
		SnapshotID string `json:"snapshotId"`
		Status     string `json:"status"`
	}
	if err := c.do(ctx, "POST", envPath(env)+"/snapshots?expand=all", "", map[string]string{"baseResourceURL": c.APIBase + envPath(env)}, &snap); err != nil {
		return nil, fmt.Errorf("snapshot of %s: %w", env, err)
	}
	if snap.SnapshotID == "" {
		return nil, errors.New("PingOne did not name the snapshot it made")
	}
	return c.ReadSnapshot(ctx, env, snap.SnapshotID)
}

// ReadSnapshot reads an existing snapshot of an environment.
func (c *Client) ReadSnapshot(ctx context.Context, env, id string) (*Snapshot, error) {
	p := envPath(env) + "/snapshots/" + url.PathEscape(env) + "/versions/" + url.PathEscape(id)
	err := c.wait(ctx, "the snapshot of "+env, func() (bool, error) {
		var s struct {
			Status string `json:"status"`
		}
		if err := c.do(ctx, "GET", p, "", nil, &s); err != nil {
			return false, err
		}
		if s.Status == "ERROR" {
			return false, errors.New("PingOne could not snapshot " + env)
		}
		return s.Status == "COMPLETE", nil
	})
	if err != nil {
		return nil, err
	}
	raw, err := c.pages(ctx, p+"/embeddedSnapshots", "referencedResources")
	if err != nil {
		return nil, err
	}
	out := &Snapshot{ID: id, Resources: map[string]Resource{}}
	for _, r := range raw {
		var s struct {
			Resource      Ref            `json:"resource"`
			URL           string         `json:"resourceUrl"`
			Configuration map[string]any `json:"configuration"`
			References    []string       `json:"referencedResources"`
		}
		if err := json.Unmarshal(r, &s); err != nil {
			return nil, err
		}
		name := s.Resource.Name
		if n, ok := s.Configuration["name"].(string); ok && name == "" {
			name = n
		}
		out.Resources[s.Resource.ID] = Resource{ID: s.Resource.ID, Name: name, URL: s.URL, Type: TypeOf(s.URL), Configuration: s.Configuration, References: s.References}
	}
	return out, nil
}

// CreatePromotion drafts a promotion of the selected source resources and
// waits for PingOne's plan. PingOne adds what the selection depends on.
func (c *Client) CreatePromotion(ctx context.Context, source, target, sourceSnapshot string, selected []string, description string) (Promotion, error) {
	var p Promotion
	body := map[string]any{"sourceSnapshot": Ref{ID: sourceSnapshot}, "targetEnvironment": Ref{ID: target}, "selectedResources": selected, "description": description}
	if err := c.do(ctx, "POST", envPath(source)+"/promotions", "", body, &p); err != nil {
		return p, err
	}
	return c.settle(ctx, source, p.ID)
}

func pending(status string) bool {
	switch status {
	case "NEW", "PREPARING", "ADDITIONAL_SNAPSHOTS_REQUESTED", "IN_PROGRESS", "ROLLBACK_NEW", "ROLLBACK_PREPARING":
		return true
	}
	return false
}

func (c *Client) settle(ctx context.Context, env, id string) (Promotion, error) {
	var p Promotion
	if id == "" {
		return p, errors.New("PingOne did not name the promotion")
	}
	err := c.wait(ctx, "promotion "+id, func() (bool, error) {
		if err := c.do(ctx, "GET", envPath(env)+"/promotions/"+url.PathEscape(id), "", nil, &p); err != nil {
			return false, err
		}
		return !pending(p.Status), nil
	})
	if err == nil && (p.Status == "ERROR" || p.Status == "VALIDATION_FAILED") {
		err = fmt.Errorf("promotion %s ended as %s: %s", id, p.Status, strings.TrimSpace(string(p.Errors)))
	}
	return p, err
}

// Steps reads the operations of a promotion's plan.
func (c *Client) Steps(ctx context.Context, env, id string) ([]Step, error) {
	raw, err := c.pages(ctx, envPath(env)+"/promotions/"+url.PathEscape(id)+"/promotionSteps", "promotionSteps")
	if err != nil {
		return nil, err
	}
	steps := make([]Step, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &steps[i]); err != nil {
			return nil, err
		}
	}
	return steps, nil
}

// Execute runs a planned promotion and waits for it to end.
func (c *Client) Execute(ctx context.Context, env, id string) (Promotion, error) {
	if err := c.do(ctx, "POST", envPath(env)+"/promotions/"+url.PathEscape(id), executeType, nil, nil); err != nil {
		return Promotion{}, err
	}
	return c.settle(ctx, env, id)
}

// Rollback drafts the rollback of a promotion: a new promotion whose plan
// undoes it. It is reviewed and executed like any other.
func (c *Client) Rollback(ctx context.Context, env, id string) (Promotion, error) {
	var p Promotion
	if err := c.do(ctx, "POST", envPath(env)+"/promotions/"+url.PathEscape(id), rollbackType, nil, &p); err != nil {
		return p, err
	}
	return c.settle(ctx, env, p.ID)
}

// TypeOf names a resource's kind from its URL
// (.../environments/<id>/flows/<id> -> flows;
// .../applications/<id>/attributes/<id> -> applications/attributes).
func TypeOf(resourceURL string) string {
	u, err := url.Parse(resourceURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if p == "environments" && i+2 < len(parts) {
			var kinds []string
			for j, seg := range parts[i+2:] {
				if j%2 == 0 { // collection names; the odd ones are IDs
					kinds = append(kinds, seg)
				}
			}
			kind := strings.Join(kinds, "/")
			if n, ok := kindNames[kind]; ok {
				return n
			}
			return kind
		}
	}
	return ""
}

// kindNames are plain names for the kinds a reviewer sees most.
var kindNames = map[string]string{"flows": "flow", "connectorInstances": "connector", "variables": "variable",
	"applications": "application", "flowPolicies": "flow policy", "populations": "population", "signOnPolicies": "sign-on policy"}
