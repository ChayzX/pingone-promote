package exporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Explicit child egress settings also apply to public image reads. This changes
// transport routing and trust only; the destination URL remains allowlisted.
func (r *ProviderStateReader) imageHTTPClient() (*http.Client, error) {
	if r.ImageHTTP != nil {
		return r.ImageHTTP, nil
	}
	if r.Network == (Network{}) {
		return nil, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy := transport.Proxy
	if r.Network.HTTPSProxy != "" {
		u, err := url.Parse(r.Network.HTTPSProxy)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Fragment != "" {
			return nil, providerReadFailure("image asset transport", "")
		}
		proxy = http.ProxyURL(u)
	}
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		host := strings.ToLower(req.URL.Hostname())
		for _, entry := range strings.Split(r.Network.NoProxy, ",") {
			entry = strings.ToLower(strings.TrimSpace(entry))
			if entry == "*" {
				return nil, nil
			}
			if hostname, port, err := net.SplitHostPort(entry); err == nil {
				if port != "443" {
					continue
				}
				entry = hostname
			}
			entry = strings.TrimPrefix(strings.TrimPrefix(entry, "*"), ".")
			if entry != "" && (host == entry || strings.HasSuffix(host, "."+entry)) {
				return nil, nil
			}
		}
		if proxy != nil {
			return proxy(req)
		}
		return nil, nil
	}
	if r.Network.CAFile != "" {
		pem, err := os.ReadFile(r.Network.CAFile)
		roots, rootsErr := x509.SystemCertPool()
		if rootsErr != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, providerReadFailure("image asset transport", "")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport}, nil
}

const imageAssetLimit = 4 << 20
const imageAssetTotalLimit = 8 << 20

// Provider 1.24.0 exposes the original target href, but does not restore the
// required upload input on import. Only that provider-owned, regional public
// asset location may supply the missing input.
var imageOriginalFilename = regexp.MustCompile(`^[A-Za-z0-9_-]{1,240}_original\.(png|gif|jpg|jpeg)$`)

func originalImageURL(href string, c Credentials) (*url.URL, bool) {
	domain := map[string]string{"NA": "com", "EU": "eu", "AP": "asia", "AU": "com.au", "CA": "ca", "SG": "sg"}[c.RegionCode]
	u, err := url.Parse(href)
	if err != nil || domain == "" || u.Scheme != "https" || u.Host != "uploads.pingone."+domain || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(href, "#") || u.RawPath != "" || u.Opaque != "" {
		return nil, false
	}
	prefix := "/environments/" + c.ExportEnvironment() + "/images/"
	if !uuidRE.MatchString(c.ExportEnvironment()) || !strings.HasPrefix(u.Path, prefix) || !imageOriginalFilename.MatchString(strings.TrimPrefix(u.Path, prefix)) {
		return nil, false
	}
	return u, true
}

func hydrateImageAssets(ctx context.Context, sourceClient *http.Client, c Credentials, state []byte, imports map[string]string) ([]byte, error) {
	var doc map[string]any
	if decodeProviderJSON(state, &doc) != nil {
		return nil, providerReadFailure("image asset state", "")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if sourceClient != nil {
		client = *sourceClient
		if client.Timeout <= 0 || client.Timeout > 30*time.Second {
			client.Timeout = 30 * time.Second
		}
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	changed, total := false, 0
	var visit func(map[string]any) error
	visit = func(module map[string]any) error {
		resources, _ := module["resources"].([]any)
		for _, raw := range resources {
			r, _ := raw.(map[string]any)
			address, _ := r["address"].(string)
			importID, requested := imports[address]
			if !requested || r["mode"] != "managed" || r["type"] != "pingone_image" || !strings.HasPrefix(address, "pingone_image.") || !addressRE.MatchString(address) {
				continue
			}
			values, _ := r["values"].(map[string]any)
			if values == nil || values["image_file_base64"] != nil {
				continue
			}
			uploaded, _ := values["uploaded_image"].(map[string]any)
			href, _ := uploaded["href"].(string)
			if href == "" {
				continue // renderer reports the missing required input
			}
			fail := func() error { return providerReadFailure("image asset", address) }
			parts := strings.Split(importID, "/")
			if len(parts) != 2 || parts[0] != c.ExportEnvironment() || !uuidRE.MatchString(parts[1]) || values["environment_id"] != parts[0] || values["id"] != parts[1] {
				return fail()
			}
			u, ok := originalImageURL(href, c)
			if !ok {
				return fail()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return fail()
			}
			resp, err := client.Do(req)
			if err != nil {
				return fail()
			}
			kind, _, mimeErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			remaining := min(imageAssetLimit, imageAssetTotalLimit-total)
			if resp.StatusCode != http.StatusOK || resp.ContentLength > int64(remaining) || mimeErr != nil || (kind != "image/png" && kind != "image/gif" && kind != "image/jpeg") {
				resp.Body.Close()
				return fail()
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(remaining)+1))
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || len(data) > remaining || http.DetectContentType(data) != kind {
				return fail()
			}
			total += len(data)
			values["image_file_base64"] = base64.StdEncoding.EncodeToString(data)
			changed = true
		}
		children, _ := module["child_modules"].([]any)
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	values, _ := doc["values"].(map[string]any)
	root, _ := values["root_module"].(map[string]any)
	if err := visit(root); err != nil {
		return nil, err
	}
	if !changed {
		return state, nil
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, providerReadFailure("image asset state", "")
	}
	return out, nil
}
