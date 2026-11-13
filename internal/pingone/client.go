// Package pingone is a READ-ONLY client for the PingOne Platform API, used
// by the native exporter (ADR 0019). It can obtain a worker token and send
// GET requests to one environment, and nothing else: there is no method
// that sends a body or another HTTP verb, so a coding mistake cannot change
// a tenant even if the worker were given write roles.
//
// Hosts (PingOne Platform API reference, "Regions"): api.pingone.<tld> and
// auth.pingone.<tld>; tld com (NA), eu (EU), ca (CA), asia (AP), com.au (AU),
// sg (SG). The AU and SG domains are assumptions to confirm in the live
// acceptance case.
package pingone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"pingone-promote/internal/netcause"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Credentials of the read-only worker application.
type Credentials struct {
	ClientID            string
	ClientSecret        string
	WorkerEnvironmentID string
	RegionCode          string
}

var tlds = map[string]string{"NA": "com", "EU": "eu", "CA": "ca", "AP": "asia", "AU": "com.au", "SG": "sg"}

var (
	uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// pathRE limits request paths to API segments (no query, no dot
	// segments, no scheme).
	pathRE = regexp.MustCompile(`^[A-Za-z0-9_\-]+(/[A-Za-z0-9_\-]+)*$`)
)

// Client reads one PingOne environment.
type Client struct {
	creds Credentials
	// AuthBase and APIBase override the regional hosts (tests).
	AuthBase, APIBase string
	HTTP              *http.Client
	// MaxBody bounds one response (default 32 MiB).
	MaxBody int64

	mu      sync.Mutex
	token   string
	expires time.Time
	// Retries bounds the retries of a rate-limited or temporarily failing
	// GET (default 6). Sleep waits between them (tests replace it).
	Retries int
	Sleep   func(context.Context, time.Duration) error
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryable reports answers worth waiting for: rate limiting (429) and
// temporary gateway or service failures. A 500 is tried three times in all
// (PingOne's own SDK retries it too).
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// getURL is getOnce with retries: a rate-limited or temporarily failing GET
// waits (the server's Retry-After, else 1, 2, 4, 8, 16 s) and tries again;
// an expired token (401) is renewed once.
func (c *Client) getURL(ctx context.Context, u, display string) (map[string]any, error) {
	retries := c.Retries
	if retries <= 0 {
		retries = 6
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	renewed := false
	for attempt := 0; ; attempt++ {
		doc, err := c.getOnce(ctx, u, display)
		var ae *APIError
		if err == nil || !errors.As(err, &ae) {
			return doc, err
		}
		if ae.Status == http.StatusUnauthorized && !renewed {
			renewed = true
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			continue
		}
		if !retryable(ae.Status) || attempt >= retries {
			return nil, err
		}
		if ae.Status == http.StatusInternalServerError && attempt >= 2 {
			return nil, err // a server error that persists is not waited out
		}
		wait := ae.retryAfter
		if wait <= 0 {
			wait = time.Second << attempt
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// New returns a client for the region of the worker.
func New(c Credentials) (*Client, error) {
	tld, ok := tlds[c.RegionCode]
	switch {
	case c.ClientID == "" || c.ClientSecret == "":
		return nil, errors.New("pingone: worker client ID and secret are required")
	case !uuidRE.MatchString(c.WorkerEnvironmentID):
		return nil, errors.New("pingone: worker environment ID must be a PingOne environment ID")
	case !ok:
		return nil, errors.New("pingone: region code must be one of AP, AU, CA, EU, NA, SG")
	}
	return &Client{creds: c, AuthBase: "https://auth.pingone." + tld, APIBase: "https://api.pingone." + tld + "/v1",
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// APIError is a non-2xx response. Body is never included: it is diagnostic
// data from the tenant and may contain configuration values.
type APIError struct {
	Status int
	Path   string
	// retryAfter is the server's Retry-After, when it sent one.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("pingone: GET %s returned HTTP %d", e.Path, e.Status)
}

// ResponseError is a 2xx answer this client cannot use (not a JSON
// object). Like APIError, it never includes the body.
type ResponseError struct {
	Path    string
	Problem string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("pingone: GET %s: %s", e.Path, e.Problem)
}

// BadResponse reports a 2xx answer that is not a JSON object.
func BadResponse(err error) bool {
	var re *ResponseError
	return errors.As(err, &re)
}

// NotFound reports a 404.
func NotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Forbidden reports a 403 (a service not licensed or a missing read role).
func Forbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthBase+"/"+c.creds.WorkerEnvironmentID+"/as/token", strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.SetBasicAuth(c.creds.ClientID, c.creds.ClientSecret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err = c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("pingone: the worker token could not be requested: " + netcause.Describe(err, req.URL.Host))
		}
		if !retryable(resp.StatusCode) || attempt >= 3 {
			break
		}
		// Busy or briefly unavailable: wait and ask again.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if err := sleep(ctx, time.Second<<attempt); err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	switch {
	case retryable(resp.StatusCode):
		return "", fmt.Errorf("pingone: PingOne is busy or unavailable (HTTP %d for the worker token); try again in a few minutes", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("pingone: the worker token request returned HTTP %d; check the worker client ID, secret, environment and region", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("pingone: the token response could not be read")
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 300
	}
	c.token, c.expires = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second-30*time.Second)
	return c.token, nil
}

func (c *Client) getOnce(ctx context.Context, u, display string) (map[string]any, error) {
	tok, err := c.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("pingone: GET %s failed: %s", display, netcause.Describe(err, req.URL.Host))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		ae := &APIError{Status: resp.StatusCode, Path: display}
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
			ae.retryAfter = time.Duration(s) * time.Second
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, ae
	}
	max := c.MaxBody
	if max <= 0 {
		max = 32 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("pingone: GET %s: reading the response failed", display)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("pingone: GET %s: the response exceeds the size limit", display)
	}
	var out map[string]any
	if strings.TrimSpace(string(body)) == "" {
		// An empty answer (some sub-resources of an object that has none)
		// holds nothing.
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, &ResponseError{Path: display, Problem: "the response is not a JSON object"}
	}
	return out, nil
}

func (c *Client) envURL(env, path string) (string, error) {
	if !uuidRE.MatchString(env) {
		return "", errors.New("pingone: environment ID must be a PingOne environment ID")
	}
	if !pathRE.MatchString(path) {
		return "", fmt.Errorf("pingone: %q is not an API path", path)
	}
	return c.APIBase + "/environments/" + env + "/" + path, nil
}

// Get reads one object (a singleton or one item).
func (c *Client) Get(ctx context.Context, env, path string) (map[string]any, error) {
	u, err := c.envURL(env, path)
	if err != nil {
		return nil, err
	}
	return c.getURL(ctx, u, path)
}

// maxPages bounds pagination.
const maxPages = 1000

// List reads every item of a collection, following _links.next on the same
// host only.
func (c *Client) List(ctx context.Context, env, path, embedded string) ([]map[string]any, error) {
	u, err := c.envURL(env, path)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(c.APIBase)
	var out []map[string]any
	for page := 0; u != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("pingone: GET %s: too many pages", path)
		}
		doc, err := c.getURL(ctx, u, path)
		if err != nil {
			return nil, err
		}
		if emb, ok := doc["_embedded"].(map[string]any); ok {
			items, _ := emb[embedded].([]any)
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
		u = ""
		if links, ok := doc["_links"].(map[string]any); ok {
			if next, ok := links["next"].(map[string]any); ok {
				if href, ok := next["href"].(string); ok && href != "" {
					nu, err := url.Parse(href)
					if err != nil || nu.Scheme != base.Scheme || nu.Host != base.Host || !strings.HasPrefix(nu.Path, base.Path+"/environments/"+env+"/") {
						return nil, fmt.Errorf("pingone: GET %s: the next page link points to another host", path)
					}
					u = nu.String()
				}
			}
		}
	}
	return out, nil
}
