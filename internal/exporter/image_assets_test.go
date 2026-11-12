package exporter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imageRoundTripper func(*http.Request) (*http.Response, error)

func (f imageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var imagePNG = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 40))

func imageState(href string) []byte {
	b, _ := json.Marshal(map[string]any{"precision": json.Number("9007199254740993"), "values": map[string]any{"root_module": map[string]any{"resources": []any{map[string]any{"address": "pingone_image.logo", "mode": "managed", "type": "pingone_image", "values": map[string]any{"environment_id": devEnv, "id": explicitImageID, "image_file_base64": nil, "uploaded_image": map[string]any{"href": href}}}}}}})
	return b
}
func imageHref() string {
	return "https://uploads.pingone.com/environments/" + devEnv + "/images/opaque_token_original.png"
}
func imageImports() map[string]string {
	return map[string]string{"pingone_image.logo": devEnv + "/" + explicitImageID}
}
func imageResponse(code int, mime string, data []byte) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}
}
func TestImageAssetHydrationCopiesPublicAssetAndPreservesState(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(imageHref())
	jar.SetCookies(u, []*http.Cookie{{Name: "private", Value: "secret"}})
	calls := 0
	client := &http.Client{Jar: jar, Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.String() != imageHref() || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("asset request leaked credentials or changed scope")
		}
		return imageResponse(200, "image/png", imagePNG), nil
	})}
	before := imageState(imageHref())
	got, err := hydrateImageAssets(context.Background(), client, creds, before, imageImports())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Contains(got, []byte(base64.StdEncoding.EncodeToString(imagePNG))) || !bytes.Contains(got, []byte("9007199254740993")) {
		t.Fatal("missing image or numeric precision lost")
	}
	if !bytes.Contains(before, []byte(`"image_file_base64":null`)) || client.Jar != jar {
		t.Fatal("caller state/client mutated")
	}
}
func TestImageAssetRejectsUnsafeURLsBeforeNetwork(t *testing.T) {
	urls := []string{"http" + strings.TrimPrefix(imageHref(), "https"), strings.Replace(imageHref(), "uploads.pingone.com", "uploads.pingone.com.evil.test", 1), strings.Replace(imageHref(), "uploads.pingone.com", "uploads.pingone.eu", 1), strings.Replace(imageHref(), "uploads.pingone.com", "user:secret@uploads.pingone.com", 1), imageHref() + "?secret=x", imageHref() + "#fragment", strings.Replace(imageHref(), devEnv, "other-env", 1), strings.Replace(imageHref(), "opaque_token_original.png", "../original.png", 1), strings.Replace(imageHref(), "opaque_token_original.png", "opaque_token.png", 1), strings.Replace(imageHref(), "opaque_token_original.png", "x%2f_original.png", 1), strings.Replace(imageHref(), "uploads.pingone.com", "uploads.pingone.com:444", 1)}
	for _, href := range urls {
		t.Run(href, func(t *testing.T) {
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("unsafe URL fetched"); return nil, nil })}
			_, err := hydrateImageAssets(context.Background(), client, creds, imageState(href), imageImports())
			var typed *ProviderReadError
			if !errors.As(err, &typed) || strings.Contains(err.Error(), href) {
				t.Fatalf("unsanitized failure %v", err)
			}
		})
	}
}
func TestImageAssetOnlyRequestedManagedImagesDownload(t *testing.T) {
	for _, mode := range []string{"unrequested", "data", "other-type", "has-value", "missing-href"} {
		t.Run(mode, func(t *testing.T) {
			state := imageState(imageHref())
			imports := imageImports()
			switch mode {
			case "unrequested":
				imports = map[string]string{}
			case "data":
				state = bytes.Replace(state, []byte(`"mode":"managed"`), []byte(`"mode":"data"`), 1)
			case "other-type":
				state = bytes.ReplaceAll(state, []byte("pingone_image"), []byte("pingone_application"))
			case "has-value":
				state = bytes.Replace(state, []byte(`"image_file_base64":null`), []byte(`"image_file_base64":"existing"`), 1)
			case "missing-href":
				state = imageState("")
			}
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("unrelated resource downloaded"); return nil, nil })}
			got, err := hydrateImageAssets(context.Background(), client, creds, state, imports)
			if err != nil || !bytes.Equal(state, got) {
				t.Fatalf("unrelated state changed: %v", err)
			}
		})
	}
}
func TestImageAssetBoundsContentAndRedirect(t *testing.T) {
	for _, kind := range []string{"mime", "magic", "oversize", "redirect", "error", "status"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				switch kind {
				case "mime":
					return imageResponse(200, "text/html", imagePNG), nil
				case "magic":
					return imageResponse(200, "image/png", []byte("<html>secret</html>")), nil
				case "oversize":
					return imageResponse(200, "image/png", append(append([]byte(nil), imagePNG...), make([]byte, 4<<20)...)), nil
				case "redirect":
					r := imageResponse(302, "image/png", nil)
					r.Header.Set("Location", "https://evil.test/secret")
					return r, nil
				case "status":
					return imageResponse(403, "image/png", []byte("secret")), nil
				default:
					return nil, errors.New("secret")
				}
			})}
			_, err := hydrateImageAssets(context.Background(), client, creds, imageState(imageHref()), imageImports())
			var typed *ProviderReadError
			if !errors.As(err, &typed) || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("bad asset accepted: %v calls=%d", err, calls)
			}
		})
	}
}
func TestImageAssetRegionAllowlist(t *testing.T) {
	for region, domain := range map[string]string{"NA": "com", "EU": "eu", "AP": "asia", "AU": "com.au", "CA": "ca", "SG": "sg"} {
		t.Run(region, func(t *testing.T) {
			c := creds
			c.RegionCode = region
			href := strings.Replace(imageHref(), "uploads.pingone.com/", "uploads.pingone."+domain+"/", 1)
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) { return imageResponse(200, "image/png", imagePNG), nil })}
			if _, err := hydrateImageAssets(context.Background(), client, c, imageState(href), imageImports()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestProviderReaderHydratesImageBeforeRendering(t *testing.T) {
	r, dir := providerFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r.ImageHTTP = &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return imageResponse(200, "image/png", imagePNG), nil
	})}
	result, err := r.Read(context.Background(), creds, []SourceResource{explicitImage()})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(result.Files[ModuleDir+"/pingone_image.tf"]), base64.StdEncoding.EncodeToString(imagePNG)) {
		t.Fatal("image asset absent from rendered output")
	}
}

func TestImageAssetRequiresCanonicalSourceIdentity(t *testing.T) {
	for _, kind := range []string{"environment", "id", "extra-import-component", "missing-id"} {
		t.Run(kind, func(t *testing.T) {
			state := imageState(imageHref())
			imports := imageImports()
			switch kind {
			case "environment":
				state = bytes.Replace(state, []byte(`"environment_id":"`+devEnv+`"`), []byte(`"environment_id":"33333333-3333-4333-8333-333333333333"`), 1)
			case "id":
				state = bytes.Replace(state, []byte(`"id":"`+explicitImageID+`"`), []byte(`"id":"33333333-3333-4333-8333-333333333333"`), 1)
			case "extra-import-component":
				imports["pingone_image.logo"] += "/extra"
			case "missing-id":
				state = bytes.Replace(state, []byte(`"id":"`+explicitImageID+`"`), []byte(`"id":null`), 1)
			}
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("noncanonical image fetched"); return nil, nil })}
			_, err := hydrateImageAssets(context.Background(), client, creds, state, imports)
			var typed *ProviderReadError
			if !errors.As(err, &typed) {
				t.Fatalf("identity accepted: %v", err)
			}
		})
	}
}

func TestImageAssetCumulativeLimitAndValidImageTypes(t *testing.T) {
	for _, tc := range []struct {
		mime string
		data []byte
	}{{"image/png", imagePNG}, {"image/gif", []byte("GIF89a" + strings.Repeat("\x00", 40))}, {"image/jpeg", []byte("\xff\xd8\xff" + strings.Repeat("\x00", 40))}} {
		t.Run(tc.mime, func(t *testing.T) {
			client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) { return imageResponse(200, tc.mime, tc.data), nil })}
			got, err := hydrateImageAssets(context.Background(), client, creds, imageState(imageHref()), imageImports())
			if err != nil || !bytes.Contains(got, []byte(base64.StdEncoding.EncodeToString(tc.data))) {
				t.Fatalf("image rejected: %v", err)
			}
		})
	}
	var doc map[string]any
	if err := json.Unmarshal(imageState(imageHref()), &doc); err != nil {
		t.Fatal(err)
	}
	root := doc["values"].(map[string]any)["root_module"].(map[string]any)
	resources := root["resources"].([]any)
	imports := imageImports()
	for _, label := range []string{"second", "third"} {
		var extra map[string]any
		if err := json.Unmarshal(imageState(imageHref()), &extra); err != nil {
			t.Fatal(err)
		}
		r := extra["values"].(map[string]any)["root_module"].(map[string]any)["resources"].([]any)[0].(map[string]any)
		address := "pingone_image." + label
		r["address"] = address
		resources = append(resources, r)
		imports[address] = devEnv + "/" + explicitImageID
	}
	root["resources"] = resources
	state, _ := json.Marshal(doc)
	calls := 0
	data := append(append([]byte(nil), imagePNG...), make([]byte, (3<<20)-len(imagePNG))...)
	client := &http.Client{Transport: imageRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return imageResponse(200, "image/png", data), nil
	})}
	_, err := hydrateImageAssets(context.Background(), client, creds, state, imports)
	var typed *ProviderReadError
	if !errors.As(err, &typed) || calls != 3 {
		t.Fatalf("cumulative image limit failed: %v calls=%d", err, calls)
	}
}

func TestImageAssetExplicitNetworkRoutesAndInjection(t *testing.T) {
	for _, tc := range []struct {
		noProxy string
		bypass  bool
	}{{"", false}, {"uploads.pingone.com", true}, {".pingone.com", true}, {"uploads.pingone.com:443", true}, {"uploads.pingone.com:444", false}, {"evilpingone.com", false}, {"*", true}} {
		t.Run(tc.noProxy, func(t *testing.T) {
			r := &ProviderStateReader{Network: Network{HTTPSProxy: "http://egress.example:8080", NoProxy: tc.noProxy}}
			client, err := r.imageHTTPClient()
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			req, _ := http.NewRequest("GET", imageHref(), nil)
			proxy, err := client.Transport.(*http.Transport).Proxy(req)
			if err != nil || (proxy == nil) != tc.bypass {
				t.Fatalf("proxy route=%v error=%v", proxy, err)
			}
		})
	}
	for _, network := range []Network{{HTTPSProxy: "file:///secret"}, {CAFile: filepath.Join(t.TempDir(), "missing")}} {
		r := &ProviderStateReader{Network: network}
		if _, err := r.imageHTTPClient(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe transport %v", err)
		}
	}
	injected := &http.Client{}
	r := &ProviderStateReader{ImageHTTP: injected, Network: Network{HTTPSProxy: "file:///secret"}}
	if got, err := r.imageHTTPClient(); err != nil || got != injected {
		t.Fatal("explicit injected client replaced")
	}
}
