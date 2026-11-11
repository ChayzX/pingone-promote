package exporter

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/resourcecatalog"
)

// SourceResource is a configuration object and its exact provider import ID.
// Values contains source configuration only in memory, for dependency discovery.
type SourceResource struct {
	Resource
	ImportID string
	Values   map[string]any
}

type InventoryReader interface {
	List(context.Context, Credentials) ([]SourceResource, error)
}

// APIInventory uses only allowlisted configuration GET endpoints. The optional
// bases are an HTTPS loopback test seam, never a tenant-controlled API setting.
type APIInventory struct {
	HTTP              *http.Client
	APIBase, AuthBase string
	// Services explicitly selects configuration families. Empty detects products
	// from the environment bill of materials. This is operator configuration.
	Services []string
}

type inventoryRecipe struct {
	Type    string          `json:"type"`
	Service string          `json:"service"`
	Steps   []inventoryStep `json:"steps"`
	Import  string          `json:"import"`
	Filter  string          `json:"filter"`
	Reason  string          `json:"reason"`
}
type inventoryStep struct {
	Path    string `json:"path"`
	Bind    string `json:"bind"`
	IDField string `json:"idField"`
	Filter  string `json:"filter"`
	Single  bool   `json:"single"`
	Bundle  bool   `json:"bundle"`
}

//go:embed inventory_recipes.json
var inventoryRecipeData []byte
var inventoryRecipes = func() []inventoryRecipe {
	var out []inventoryRecipe
	if json.Unmarshal(inventoryRecipeData, &out) != nil {
		panic("invalid inventory recipes")
	}
	for _, r := range out {
		if !resourcecatalog.Allowed(r.Type) {
			panic("inventory recipe outside configuration allowlist")
		}
		if r.Service != "" && !inventoryServiceFamilies[r.Service] {
			panic("inventory recipe has an unknown service")
		}
		for _, s := range r.Steps {
			if !strings.HasPrefix(s.Path, "/environments/{environmentID}") || unsafeInventoryPath(s.Path) {
				panic("unsafe inventory recipe")
			}
		}
	}
	return out
}()

// InventoryLimitations documents types that cannot be listed or imported.
func InventoryLimitations() map[string]string {
	out := map[string]string{}
	for _, r := range inventoryRecipes {
		if r.Reason != "" {
			out[r.Type] = r.Reason
		}
	}
	return out
}

// InventoryHTTPError deliberately excludes response bodies, URLs, and tokens.
// 403 (including missing service entitlements) and 404 abort inventory; they are
// never silently treated as an empty configuration collection.
type InventoryHTTPError struct {
	ResourceType string
	StatusCode   int
}

func (e *InventoryHTTPError) Error() string {
	return fmt.Sprintf("exporter: inventory %s returned HTTP %d", e.ResourceType, e.StatusCode)
}

type inventorySession struct {
	ctx      context.Context
	client   *http.Client
	base     string
	token    string
	env      string
	cache    map[string]map[string]any
	pages    map[string][]map[string]any
	requests int
	bytes    int64
}

func (a *APIInventory) session(ctx context.Context, c Credentials) (*inventorySession, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if _, err := a.explicitServices(); err != nil {
		return nil, err
	}
	domain := map[string]string{"AP": "asia", "AU": "com.au", "CA": "ca", "EU": "eu", "NA": "com", "SG": "sg"}[c.RegionCode]
	api, auth := "https://api.pingone."+domain+"/v1", "https://auth.pingone."+domain
	if a.APIBase != "" || a.AuthBase != "" {
		if !inventoryLoopbackBase(a.APIBase) || !inventoryLoopbackBase(a.AuthBase) {
			return nil, errors.New("exporter: inventory override requires HTTPS loopback bases")
		}
		api, auth = strings.TrimRight(a.APIBase, "/"), strings.TrimRight(a.AuthBase, "/")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if a.HTTP != nil {
		client = *a.HTTP
		if client.Timeout <= 0 || client.Timeout > 30*time.Second {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, auth+"/"+c.WorkerEnvironmentID+"/as/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return nil, errors.New("exporter: could not construct inventory authentication request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("exporter: inventory authentication request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &InventoryHTTPError{ResourceType: "authentication", StatusCode: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, errors.New("exporter: invalid inventory authentication response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(b, &token) != nil || token.AccessToken == "" || strings.ContainsAny(token.AccessToken, "\r\n") || (token.TokenType != "" && !strings.EqualFold(token.TokenType, "Bearer")) {
		return nil, errors.New("exporter: invalid inventory authentication response")
	}
	return &inventorySession{ctx: ctx, client: &client, base: api, token: token.AccessToken, env: c.ExportEnvironment(), cache: map[string]map[string]any{}, pages: map[string][]map[string]any{}}, nil
}
func inventoryLoopbackBase(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}
func unsafeInventoryPath(p string) bool {
	for _, s := range strings.Split(strings.ToLower(p), "/") {
		switch s {
		case "users", "sessions", "activities", "audit", "events", "members":
			return true
		}
	}
	return false
}

var inventoryServiceFamilies = map[string]bool{"base": true, "sso": true, "mfa": true, "risk": true, "authorize": true, "credentials": true, "verify": true, "davinci": true}

func (a *APIInventory) explicitServices() (map[string]bool, error) {
	if len(a.Services) == 0 {
		return nil, nil
	}
	out := map[string]bool{}
	for _, family := range a.Services {
		if !inventoryServiceFamilies[family] {
			return nil, errors.New("exporter: unknown pingone.services configuration family")
		}
		out[family] = true
	}
	return out, nil
}
func (a *APIInventory) enabledServices(s *inventorySession) (map[string]bool, error) {
	explicit, err := a.explicitServices()
	if err != nil || explicit != nil {
		return explicit, err
	}
	bom, err := s.get("/environments/"+s.env+"/billOfMaterials", "billOfMaterials")
	if err != nil {
		return nil, fmt.Errorf("exporter: cannot detect enabled services; configure pingone.services explicitly: %w", err)
	}
	products, ok := bom["products"].([]any)
	if !ok {
		return nil, errors.New("exporter: invalid bill of materials; configure pingone.services explicitly")
	}
	// The default platform product supplies base and SSO configuration services.
	enabled := map[string]bool{"base": true, "sso": true}
	for _, raw := range products {
		product, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("exporter: invalid bill of materials product; configure pingone.services explicitly")
		}
		kind := inventoryString(product, "type")
		if kind == "" {
			return nil, errors.New("exporter: invalid bill of materials product type; configure pingone.services explicitly")
		}
		switch kind {
		case "PING_ONE_BASE":
			enabled["base"] = true
			enabled["sso"] = true
		case "PING_ONE_MFA":
			enabled["mfa"] = true
		case "PING_ONE_RISK", "PING_ONE_FRAUD":
			enabled["risk"] = true
		case "PING_ONE_AUTHORIZE":
			enabled["authorize"] = true
		case "PING_ONE_CREDENTIALS":
			enabled["credentials"] = true
		case "PING_ONE_VERIFY":
			enabled["verify"] = true
		case "PING_ONE_DAVINCI", "PING_ONE_ORCHESTRATE":
			enabled["davinci"] = true
		}
	}
	return enabled, nil
}

// ServiceEnabled uses the same licensed-service decision as List. Explicit
// operator configuration avoids the bill-of-materials request entirely.
func (a *APIInventory) ServiceEnabled(ctx context.Context, c Credentials, family string) (bool, error) {
	if !inventoryServiceFamilies[family] {
		return false, errors.New("exporter: unknown inventory configuration family")
	}
	if err := c.validate(); err != nil {
		return false, err
	}
	explicit, err := a.explicitServices()
	if err != nil {
		return false, err
	}
	if explicit != nil {
		return explicit[family], nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	s, err := a.session(ctx, c)
	if err != nil {
		return false, err
	}
	enabled, err := a.enabledServices(s)
	if err != nil {
		return false, err
	}
	return enabled[family], nil
}

func (s *inventorySession) get(path, typ string) (map[string]any, error) {
	if v, ok := s.cache[path]; ok {
		return v, nil
	}
	if s.requests >= 10000 {
		return nil, errors.New("exporter: inventory request limit exceeded")
	}
	s.requests++
	if unsafeInventoryPath(path) {
		return nil, errors.New("exporter: inventory path is not configuration")
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, errors.New("exporter: invalid inventory request")
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("exporter: inventory request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &InventoryHTTPError{ResourceType: typ, StatusCode: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return nil, errors.New("exporter: inventory response exceeds limit or could not be read")
	}
	s.bytes += int64(len(b))
	if s.bytes > 64<<20 {
		return nil, errors.New("exporter: inventory total response limit exceeded")
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil || v == nil {
		return nil, errors.New("exporter: invalid inventory JSON response")
	}
	s.cache[path] = v
	return v, nil
}
func (s *inventorySession) records(path, typ string, single bool) ([]map[string]any, error) {
	if single {
		v, e := s.get(path, typ)
		if e != nil {
			return nil, e
		}
		return []map[string]any{v}, nil
	}
	if v, ok := s.pages[path]; ok {
		return v, nil
	}
	initial, e := url.Parse(s.base + path)
	if e != nil {
		return nil, errors.New("exporter: invalid inventory path")
	}
	current := path
	seen := map[string]bool{}
	out := []map[string]any{}
	for page := 0; page < 1000; page++ {
		if seen[current] {
			return nil, errors.New("exporter: cyclic inventory pagination")
		}
		seen[current] = true
		v, err := s.get(current, typ)
		if err != nil {
			return nil, err
		}
		embedded, ok := v["_embedded"].(map[string]any)
		if !ok {
			return nil, errors.New("exporter: inventory collection lacks an embedded object")
		}
		keys := make([]string, 0, len(embedded))
		for k := range embedded {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			items, ok := embedded[k].([]any)
			if !ok {
				return nil, errors.New("exporter: invalid inventory collection shape")
			}
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					return nil, errors.New("exporter: invalid inventory collection item")
				}
				out = append(out, m)
				if len(out) > 100000 {
					return nil, errors.New("exporter: inventory object limit exceeded")
				}
			}
		}
		next := inventoryString(v, "_links.next.href")
		if next == "" {
			s.pages[path] = out
			return out, nil
		}
		n, err := url.Parse(next)
		if err != nil {
			return nil, errors.New("exporter: invalid inventory pagination")
		}
		n = initial.ResolveReference(n)
		if n.Scheme != initial.Scheme || n.Host != initial.Host || n.Path != initial.Path || n.User != nil || n.Fragment != "" {
			return nil, errors.New("exporter: inventory pagination left its collection")
		}
		// Only opaque cursor/offset pagination parameters are accepted. Credentials,
		// filters, expansions, and endpoint substitutions cannot arrive via links.
		query, err := url.ParseQuery(n.RawQuery)
		if err != nil || len(next) > 8192 {
			return nil, errors.New("exporter: invalid inventory pagination query")
		}
		for key := range query {
			switch key {
			case "cursor", "limit", "offset", "after", "before", "page":
			default:
				return nil, errors.New("exporter: unsupported inventory pagination parameter")
			}
		}
		current = strings.TrimPrefix(n.String(), s.base)
	}
	return nil, errors.New("exporter: inventory pagination limit exceeded")
}
func inventoryString(v map[string]any, path string) string {
	var x any = v
	for _, k := range strings.Split(path, ".") {
		m, ok := x.(map[string]any)
		if !ok {
			return ""
		}
		x = m[k]
	}
	st, _ := x.(string)
	return st
}

var inventoryPlaceholder = regexp.MustCompile(`\{([^{}]+)\}`)

func inventoryExpand(t string, vars map[string]string, v map[string]any) (string, error) {
	missing := false
	out := inventoryPlaceholder.ReplaceAllStringFunc(t, func(x string) string {
		k := x[1 : len(x)-1]
		str := vars[k]
		if str == "" {
			str = inventoryString(v, k)
		}
		if str == "" || strings.ContainsAny(str, "/\\?#\x00\r\n") || str == "." || str == ".." {
			missing = true
			return ""
		}
		return url.PathEscape(str)
	})
	if missing {
		return "", errors.New("exporter: inventory object lacks a safe import or parent identifier")
	}
	return out, nil
}
func inventoryAccept(filter string, v map[string]any) bool {
	name := inventoryString(v, "name")
	typ := inventoryString(v, "type")
	def, _ := v["default"].(bool)
	system := strings.HasPrefix(typ, "PING_ONE_")
	switch filter {
	case "application":
		return !system
	case "system":
		return typ == "PING_ONE_PORTAL" || typ == "PING_ONE_SELF_SERVICE"
	case "custom_resource":
		return typ == "CUSTOM" || (typ == "" && name != "openid" && name != "PingOne API")
	case "openid":
		return typ == "OPENID_CONNECT" || name == "openid"
	case "pingone_api":
		return typ == "PINGONE_API" || name == "PingOne API"
	case "default":
		return def
	case "nondefault":
		return !def
	case "noncore":
		return inventoryString(v, "schemaType") != "CORE"
	case "application_secret":
		protocol := inventoryString(v, "protocol")
		return !system && (protocol == "OPENID_CONNECT" || protocol == "SAML" || protocol == "EXTERNAL_LINK")
	case "resource_secret":
		return typ == "CUSTOM" || (typ == "" && name != "openid" && name != "PingOne API")
	case "metadata":
		return v["metadata"] != nil
	case "deployment":
		return inventoryString(v, "status.code") != "DEPLOYMENT_UNINITIALIZED"
	case "custom_language":
		return !inventoryReservedLocale(inventoryString(v, "locale"))
	case "system_language":
		return inventoryReservedLocale(inventoryString(v, "locale"))
	}
	return true
}

type inventoryObject struct {
	recipe  inventoryRecipe
	value   map[string]any
	parents []map[string]any
	vars    map[string]string
}

func (s *inventorySession) collect(r inventoryRecipe, step int, vars map[string]string, parents []map[string]any) ([]inventoryObject, error) {
	if step == len(r.Steps) {
		return nil, nil
	}
	st := r.Steps[step]
	p, err := inventoryExpand(st.Path, vars, nil)
	if err != nil {
		return nil, err
	}
	records, err := s.records(p, r.Type, st.Single)
	if err != nil {
		return nil, err
	}
	if st.Bundle {
		records = []map[string]any{{"name": vars["locale"], "id": vars["locale"], "translations": records}}
	}
	out := []inventoryObject{}
	for _, v := range records {
		if !inventoryAccept(st.Filter, v) {
			continue
		}
		if step+1 == len(r.Steps) {
			if inventoryAccept(r.Filter, v) {
				out = append(out, inventoryObject{r, v, parents, vars})
			}
			continue
		}
		next := map[string]string{}
		for k, v := range vars {
			next[k] = v
		}
		field := st.IDField
		if field == "" {
			field = "id"
		}
		id := inventoryString(v, field)
		if id == "" {
			return nil, errors.New("exporter: inventory collection item lacks a parent identifier")
		}
		next[st.Bind] = id
		nested, err := s.collect(r, step+1, next, append(append([]map[string]any{}, parents...), v))
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}
	return out, nil
}

func (a *APIInventory) List(ctx context.Context, c Credentials) ([]SourceResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	s, err := a.session(ctx, c)
	if err != nil {
		return nil, err
	}
	enabled, err := a.enabledServices(s)
	if err != nil {
		return nil, err
	}
	objects := []inventoryObject{}
	for _, r := range inventoryRecipes {
		catalog, _ := resourcecatalog.Lookup(r.Type)
		family := catalog.Family
		if r.Service != "" {
			family = r.Service
		}
		if !enabled[family] {
			continue
		}
		if len(r.Steps) == 0 {
			continue
		}
		got, err := s.collect(r, 0, map[string]string{"environmentID": s.env}, nil)
		if err != nil {
			return nil, err
		}
		objects = append(objects, got...)
	}
	// Names of referenced configuration objects let assignments use semantic
	// labels consistently across source environments with different IDs.
	names := map[string]string{}
	for _, o := range objects {
		if id := inventoryString(o.value, "id"); id != "" {
			if n := inventoryObjectName(o.value, nil); n != "" {
				names[id] = n
			}
		}
	}
	// Resolve built-in administrative roles only through the SDK's fixed
	// configuration endpoint; never follow an object-provided URL.
	for _, o := range objects {
		if roleID := inventoryString(o.value, "role.id"); roleID != "" && names[roleID] == "" {
			if !uuidRE.MatchString(roleID) {
				return nil, errors.New("exporter: role reference is not a PingOne resource ID")
			}
			role, err := s.get("/roles/"+roleID, o.recipe.Type)
			if err != nil {
				return nil, err
			}
			names[roleID] = inventoryObjectName(role, nil)
		}
	}
	out := []SourceResource{}
	labels := map[string]bool{}
	for _, o := range objects {
		imp, err := inventoryExpand(o.recipe.Import, o.vars, o.value)
		if err != nil {
			return nil, err
		}
		n := inventoryObjectName(o.value, names)
		if n == "" && len(o.parents) > 0 {
			if !strings.Contains(o.recipe.Import, "{id}") {
				n = strings.TrimPrefix(o.recipe.Type, "pingone_")
			} else {
				n = inventoryContentName(o.value, names)
			}
		}
		if n == "" && o.recipe.Import == "{environmentID}" {
			n = strings.TrimPrefix(o.recipe.Type, "pingone_")
		}
		if n == "" {
			n = inventoryContentName(o.value, names)
		}
		parts := []string{}
		for _, p := range o.parents {
			pn := inventoryObjectName(p, names)
			if pn == "" {
				return nil, fmt.Errorf("exporter: inventory %s parent lacks a stable configuration name", o.recipe.Type)
			}
			parts = append(parts, pn)
		}
		labelName := n
		if o.recipe.Import == "{environmentID}" {
			labelName = strings.TrimPrefix(o.recipe.Type, "pingone_")
		}
		parts = append(parts, labelName)
		if strings.HasSuffix(o.recipe.Type, "_role_assignment") {
			scopeType := inventoryString(o.value, "scope.type")
			scopeName := inventoryString(o.value, "scope.name")
			if scopeName == "" {
				scopeName = names[inventoryString(o.value, "scope.id")]
			}
			switch scopeType {
			case "ENVIRONMENT":
				scopeName = "environment"
			case "ORGANIZATION":
				scopeName = "organization"
			}
			if scopeType != "" {
				if scopeName == "" {
					return nil, errors.New("exporter: role scope lacks a stable configuration name")
				}
				parts = append(parts, scopeType, scopeName)
			}
		}
		label := inventoryLabel(strings.Join(parts, "__"))
		addr := o.recipe.Type + "." + label
		if labels[addr] {
			return nil, &LabelCollisionError{Detail: "configuration inventory has duplicate stable labels for " + o.recipe.Type}
		}
		labels[addr] = true
		id := inventoryString(o.value, "id")
		if id == "" {
			id = imp[strings.LastIndex(imp, "/")+1:]
		}
		vals := map[string]any{}
		for k, v := range o.value {
			vals[k] = v
		}
		if len(o.parents) > 0 {
			refs := map[string]any{}
			for binding, field := range map[string]string{"applicationID": "application", "resourceID": "resource", "agreementID": "agreement", "languageID": "language", "gatewayID": "gateway", "groupID": "group", "providerID": "identityProvider", "applicationResourceID": "applicationResource", "applicationRoleID": "applicationRole", "apiServerID": "apiServer", "schemaID": "schema", "policyID": "signOnPolicy", "credentialTypeID": "credentialType", "voicePhraseID": "voicePhrase", "emailDomainID": "emailDomain"} {
				if id := o.vars[binding]; id != "" {
					refs[field] = map[string]any{"id": id}
				}
			}
			if len(refs) > 0 {
				vals["_inventoryParentReferences"] = refs
			}
		}
		out = append(out, SourceResource{Resource: Resource{Address: Address{Type: o.recipe.Type, Label: label}, Name: n, ID: id}, ImportID: imp, Values: vals})
	}
	// Images have no list endpoint in the pinned SDK. Discover references only
	// from the already fetched configuration, never by following arbitrary URLs.
	images := map[string]string{}
	if enabled["base"] {
		for _, o := range out {
			inventoryImages(o.Values, o.Label, images)
		}
	}
	imageIDs := make([]string, 0, len(images))
	for id := range images {
		imageIDs = append(imageIDs, id)
	}
	sort.Strings(imageIDs)
	for _, id := range imageIDs {
		r, err := s.lookupImage(id)
		if err != nil {
			return nil, err
		}
		r.Name = images[id]
		r.Label = inventoryLabel(images[id])
		if labels[r.String()] {
			return nil, &LabelCollisionError{Detail: "configuration inventory has duplicate image names"}
		}
		labels[r.String()] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
func inventoryObjectName(v map[string]any, names map[string]string) string {
	if locale := inventoryString(v, "locale"); locale != "" && inventoryString(v, "deliveryMethod") != "" {
		return locale + "__" + inventoryString(v, "deliveryMethod") + "__" + inventoryString(v, "variant")
	}
	if n := inventoryString(v, "configuration.name"); n != "" {
		return n
	}
	for _, k := range []string{"name", "displayName", "locale", "domainName", "emailAddress", "nickname", "subjectDN", "alias", "keyId"} {
		if n := inventoryString(v, k); n != "" {
			return n
		}
	}
	for _, k := range []string{"role", "resource", "group", "scope", "signOnPolicy", "flowPolicy", "identityProvider", "permission"} {
		if n := inventoryString(v, k+".name"); n != "" {
			return n
		}
		if n := names[inventoryString(v, k+".id")]; n != "" {
			return n
		}
	}
	return inventoryString(v, "type")
}

// Unnamed configuration (for example agreement revisions) uses a content
// digest with volatile fields and source IDs removed. Collisions fail closed.
func inventoryContentName(v map[string]any, names map[string]string) string {
	var clean func(any) any
	clean = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, val := range t {
				switch k {
				case "_links", "environment", "createdAt", "updatedAt":
					continue
				case "id":
					if n := names[inventoryString(t, "id")]; n != "" {
						out["name"] = n
					}
					continue
				}
				out[k] = clean(val)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, v := range t {
				out[i] = clean(v)
			}
			return out
		case string:
			if uuidRE.MatchString(t) {
				return names[t]
			}
		}
		return x
	}
	b, _ := json.Marshal(clean(v))
	sum := sha256.Sum256(b)
	return "configuration_" + hex.EncodeToString(sum[:8])
}
func inventoryLabel(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	label := b.String()
	if label == "" {
		label = "configuration"
	}
	if label[0] >= '0' && label[0] <= '9' {
		label = "r_" + label
	}
	return label
}
func inventoryReservedLocale(locale string) bool {
	return strings.Contains("|cs|de|en|es|fr-CA|fr|hu|it|ja|ko|nl|pl|pt|ru|th|tr|zh|", "|"+locale+"|")
}
func inventoryImages(v any, owner string, ids map[string]string) {
	var walk func(any, string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				walk(val, path+"__"+k)
			}
		case []any:
			for i, val := range x {
				walk(val, fmt.Sprintf("%s_%d", path, i))
			}
		case string:
			lower := strings.ToLower(path)
			if !strings.Contains(lower, "image") && !strings.Contains(lower, "icon") && !strings.Contains(lower, "logo") {
				return
			}
			id := ""
			if uuidRE.MatchString(x) {
				id = x
			} else if u, err := url.Parse(x); err == nil {
				parts := strings.Split(u.Path, "/")
				for i, p := range parts {
					if p == "images" && i+1 < len(parts) && uuidRE.MatchString(parts[i+1]) {
						id = parts[i+1]
					}
				}
			}
			if id != "" {
				label := owner + strings.TrimSuffix(strings.TrimSuffix(path, "__id"), "__href")
				if prev := ids[id]; prev == "" || label < prev {
					ids[id] = label
				}
			}
		}
	}
	walk(v, "")
}
func (s *inventorySession) lookupImage(id string) (SourceResource, error) {
	if !uuidRE.MatchString(id) {
		return SourceResource{}, errors.New("exporter: image ID must be a PingOne resource ID")
	}
	v, err := s.get("/environments/"+s.env+"/images/"+id, "pingone_image")
	if err != nil {
		return SourceResource{}, err
	}
	n := inventoryObjectName(v, nil)
	if n == "" {
		n = inventoryString(v, "fileName")
	}
	if n == "" {
		n = "image" // Explicit ID lookup has no semantic owner; List assigns the reference label.
	}
	return SourceResource{Resource: Resource{Address: Address{Type: "pingone_image", Label: inventoryLabel(n)}, Name: n, ID: id}, ImportID: s.env + "/" + id, Values: v}, nil
}

// Lookup supports explicit image IDs because PingOne exposes no image list API.
func (a *APIInventory) Lookup(ctx context.Context, c Credentials, typ, id string) (SourceResource, error) {
	if typ != "pingone_image" {
		return SourceResource{}, errors.New("exporter: explicit API lookup supports images only")
	}
	if !uuidRE.MatchString(id) {
		return SourceResource{}, errors.New("exporter: image ID must be a PingOne resource ID")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	s, err := a.session(ctx, c)
	if err != nil {
		return SourceResource{}, err
	}
	enabled, err := a.enabledServices(s)
	if err != nil {
		return SourceResource{}, err
	}
	if !enabled["base"] {
		return SourceResource{}, errors.New("exporter: base configuration service is not enabled")
	}
	return s.lookupImage(id)
}
