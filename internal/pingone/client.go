package pingone

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
	"sync"
	"time"
)

type Client struct {
	BaseURL     string
	TokenURL    string
	Profile     Profile
	Secret      string
	HTTP        *http.Client
	AccessToken string
	mu          sync.Mutex
	expires     time.Time
}

func NewClient(p Profile, secret string) (*Client, error) {
	domain, ok := RegionDomains[p.Region]
	if !ok {
		return nil, errors.New("choose a supported PingOne region")
	}
	auth := p.AuthEnvironmentID
	if auth == "" {
		auth = p.EnvironmentID
	}
	return &Client{BaseURL: "https://api." + domain, TokenURL: "https://auth." + domain + "/" + url.PathEscape(auth) + "/as/token", Profile: p, Secret: secret, HTTP: &http.Client{Timeout: 45 * time.Second}}, nil
}
func (c *Client) transport() *http.Client {
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 45 * time.Second}
	}
	clone := *h
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("API redirects are not allowed") }
	return &clone
}
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.AccessToken != "" && (c.expires.IsZero() || time.Now().Before(c.expires)) {
		return c.AccessToken, nil
	}
	if c.Secret == "" {
		return "", errors.New("credential is unavailable; unlock the profile or set its secret environment variable")
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(url.QueryEscape(c.Profile.ClientID), url.QueryEscape(c.Secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.transport().Do(req)
	if err != nil {
		return "", errors.New("OAuth request failed; check region and authentication environment")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("OAuth returned HTTP %d; check worker credentials and authentication environment", res.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Expires     int    `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("OAuth returned an invalid token response")
	}
	c.AccessToken = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(max(tok.Expires-30, 1)) * time.Second)
	return c.AccessToken, nil
}
func (c *Client) apiURL(path string) (*url.URL, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	u = base.ResolveReference(u)
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/v1/environments/") || strings.Contains(u.EscapedPath(), "%") {
		return nil, errors.New("refused API URL outside the configured PingOne origin or environment path")
	}
	return u, nil
}
func (c *Client) Do(ctx context.Context, method, path, contentType string, body, output any) error {
	u, err := c.apiURL(path)
	if err != nil {
		return err
	}
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if contentType == "" && body != nil {
		contentType = "application/json"
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.transport().Do(req)
	if err != nil {
		return errors.New("PingOne request did not return a response; refresh status before retrying any operation")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if err != nil {
		return errors.New("could not read PingOne response")
	}
	if len(raw) > 32<<20 {
		return errors.New("PingOne response exceeded 32 MiB; narrow the selection")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		message := strings.ReplaceAll(e.Message, c.Secret, "[redacted]")
		if c.Secret == "" {
			message = e.Message
		}
		if tok != "" {
			message = strings.ReplaceAll(message, tok, "[redacted]")
		}
		// The API gateway answers unknown routes by reflecting a hash of the
		// Authorization header. Never relay credential-derived text into logs.
		if strings.Contains(strings.ToLower(message), "authorization") {
			message = "request rejected by the PingOne API gateway (unknown route or unsupported method); details withheld because they reference request credentials"
		}
		if len(message) > 700 {
			message = message[:700]
		}
		return fmt.Errorf("PingOne HTTP %d (%s): %s", res.StatusCode, e.Code, message)
	}
	if output != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err = json.Unmarshal(raw, output); err != nil {
			return errors.New("PingOne returned malformed JSON")
		}
	}
	return nil
}
func (c *Client) Pages(ctx context.Context, path, key string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	seen := map[string]bool{}
	first, err := c.apiURL(path)
	if err != nil {
		return nil, err
	}
	next := first.String()
	expected := -1
	for page := 0; next != ""; page++ {
		if page >= 10000 || seen[next] {
			return nil, errors.New("cyclic or excessive PingOne pagination")
		}
		seen[next] = true
		u, err := c.apiURL(next)
		if err != nil {
			return nil, err
		}
		if u.Path != first.Path {
			return nil, errors.New("pagination changed resource collection")
		}
		var out struct {
			Embedded map[string]json.RawMessage `json:"_embedded"`
			Links    map[string]struct {
				Href string `json:"href"`
			} `json:"_links"`
			Count *int `json:"count"`
		}
		if err = c.Do(ctx, "GET", u.String(), "", nil, &out); err != nil {
			return nil, err
		}
		raw, ok := out.Embedded[key]
		if !ok || len(raw) == 0 || raw[0] != '[' {
			return nil, fmt.Errorf("incomplete PingOne response: missing %s collection", key)
		}
		var items []json.RawMessage
		if err = json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if out.Count != nil {
			expected = max(expected, *out.Count)
		}
		next = ""
		if link := out.Links["next"].Href; link != "" {
			ref, err := url.Parse(link)
			if err != nil {
				return nil, err
			}
			next = u.ResolveReference(ref).String()
		}
	}
	if expected >= 0 && len(all) < expected {
		return nil, fmt.Errorf("incomplete PingOne pagination: received %d of %d resources", len(all), expected)
	}
	return all, nil
}
func (c *Client) GetPromotion(ctx context.Context, env, id string) (Promotion, error) {
	var p Promotion
	err := c.Do(ctx, "GET", EnvPath(env)+"/promotions/"+url.PathEscape(id), "", nil, &p)
	if err == nil && p.ID != id {
		err = errors.New("promotion identity did not match request")
	}
	return p, err
}
func (c *Client) Steps(ctx context.Context, env, id string) ([]Step, error) {
	raw, err := c.Pages(ctx, EnvPath(env)+"/promotions/"+url.PathEscape(id)+"/promotionSteps", "promotionSteps")
	if err != nil {
		return nil, err
	}
	steps := make([]Step, 0, len(raw))
	for _, r := range raw {
		var s Step
		if err = json.Unmarshal(r, &s); err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, nil
}
func EnvPath(env string) string { return "/v1/environments/" + url.PathEscape(env) }
