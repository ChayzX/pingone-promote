package exporter

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"pingone-promote/internal/resourcecatalog"
)

// RenderProviderState converts supplied Terraform schema/show JSON into HCL.
// It never invokes Terraform or contacts PingOne. State and schema sensitivity
// are both authoritative; secret values never become file contents or errors.
func RenderProviderState(schemaJSON, stateJSON []byte, imports map[string]string) (*Export, error) {
	var schemaDoc struct {
		Providers map[string]struct {
			Resources map[string]resourceSchema `json:"resource_schemas"`
		} `json:"provider_schemas"`
	}
	if err := decodeProviderJSON(schemaJSON, &schemaDoc); err != nil {
		return nil, errors.New("provider state: invalid schema JSON")
	}
	provider, ok := schemaDoc.Providers["registry.terraform.io/pingidentity/pingone"]
	if !ok {
		return nil, errors.New("provider state: PingOne provider schema is missing")
	}
	var state struct {
		Values struct {
			Root *stateModule `json:"root_module"`
		} `json:"values"`
	}
	if err := decodeProviderJSON(stateJSON, &state); err != nil {
		return nil, errors.New("provider state: invalid state JSON")
	}
	if state.Values.Root == nil {
		return nil, errors.New("provider state: root_module is missing")
	}
	resources := map[string]stateResource{}
	if err := collectProviderState(state.Values.Root, resources); err != nil {
		return nil, err
	}
	// Validate every supplied import, even when there are no matching resources.
	for addr, id := range imports {
		if _, ok := resources[addr]; !ok {
			return nil, errors.New("provider state: import address is not a rendered resource")
		}
		if id == "" || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return nil, errors.New("provider state: invalid import ID")
		}
	}
	renderer := stateRenderer{vars: map[string]secretVariable{}}
	files := map[string][]byte{}
	for _, addr := range sortedKeys(resources) {
		r := resources[addr]
		s, ok := provider.Resources[r.Type]
		if !ok {
			return nil, fmt.Errorf("provider state: no schema for managed resource type %q", r.Type)
		}
		if err := validateSensitivity(r.Values, r.Sensitive); err != nil {
			return nil, fmt.Errorf("provider state: %s: malformed sensitivity metadata", addr)
		}
		f := hclwrite.NewEmptyFile()
		b := f.Body().AppendNewBlock("resource", []string{r.Type, strings.Split(addr, ".")[1]})
		if err := renderer.body(b.Body(), r.Values, r.Sensitive, s.Block, addr); err != nil {
			return nil, err
		}
		name := ModuleDir + "/" + r.Type + ".tf"
		files[name] = append(files[name], f.Bytes()...)
	}
	// The environment input is needed in both scopes, even for an empty export.
	files[ModuleDir+"/variables.tf"] = renderer.variables()
	files[ModuleName+"-variables.tf"] = renderer.variables()
	files[ModuleDir+"/versions.tf"] = []byte("terraform {\n  required_providers {\n    pingone = {\n      source = \"pingidentity/pingone\"\n      version = \"= " + resourcecatalog.ProviderVersion() + "\"\n    }\n  }\n}\n")
	moduleFile := hclwrite.NewEmptyFile()
	mod := moduleFile.Body().AppendNewBlock("module", []string{ModuleName})
	mod.Body().SetAttributeValue("source", cty.StringVal("./"+ModuleDir))
	mod.Body().SetAttributeTraversal("pingone_environment_id", hclTraversal("var", "pingone_environment_id"))
	for _, name := range sortedKeys(renderer.vars) {
		mod.Body().SetAttributeTraversal(name, hclTraversal("var", name))
	}
	files[ModuleName+"-module.tf"] = moduleFile.Bytes()
	importsFile := hclwrite.NewEmptyFile()
	for _, addr := range sortedKeys(imports) {
		parts := strings.Split(addr, ".")
		b := importsFile.Body().AppendNewBlock("import", nil)
		b.Body().SetAttributeTraversal("to", hclTraversal("module", ModuleName, parts[0], parts[1]))
		b.Body().SetAttributeValue("id", cty.StringVal(imports[addr]))
	}
	files[ModuleName+"-imports.tf"] = importsFile.Bytes()
	files[ModuleName+"-terraform.auto.tfvars"] = []byte("# Values are supplied by the promotion environment.\n")
	return &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: files}, nil
}

func decodeProviderJSON(src []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(src))
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

type resourceSchema struct {
	Block schemaBlock `json:"block"`
}
type schemaBlock struct {
	Attributes map[string]schemaAttribute `json:"attributes"`
	Blocks     map[string]nestedBlock     `json:"block_types"`
}
type schemaAttribute struct {
	Type      json.RawMessage  `json:"type"`
	Required  bool             `json:"required"`
	Optional  bool             `json:"optional"`
	Computed  bool             `json:"computed"`
	Sensitive bool             `json:"sensitive"`
	Nested    *nestedAttribute `json:"nested_type"`
}
type nestedAttribute struct {
	Nesting    string                     `json:"nesting_mode"`
	Attributes map[string]schemaAttribute `json:"attributes"`
}
type nestedBlock struct {
	Nesting  string      `json:"nesting_mode"`
	Block    schemaBlock `json:"block"`
	MinItems int         `json:"min_items"`
	MaxItems int         `json:"max_items"`
}
type stateModule struct {
	Resources []stateResource `json:"resources"`
	Children  []*stateModule  `json:"child_modules"`
}
type stateResource struct {
	Address   string         `json:"address"`
	Mode      string         `json:"mode"`
	Type      string         `json:"type"`
	Values    map[string]any `json:"values"`
	Sensitive any            `json:"sensitive_values"`
}

var providerIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func collectProviderState(m *stateModule, out map[string]stateResource) error {
	for _, r := range m.Resources {
		if r.Mode == "data" {
			continue
		}
		if r.Mode != "managed" {
			return errors.New("provider state: unsupported resource mode")
		}
		parts := strings.Split(r.Address, ".")
		// The isolated importer deliberately uses unindexed root addresses. Do
		// not flatten module/count/for_each addresses and silently alias objects.
		if len(parts) != 2 || parts[0] != r.Type || !providerIdentifier.MatchString(parts[1]) || r.Values == nil {
			return errors.New("provider state: unsupported managed resource address or values")
		}
		if !resourcecatalog.Allowed(r.Type) {
			return errors.New("provider state: resource type is outside the configuration catalog")
		}
		if _, exists := out[r.Address]; exists {
			return errors.New("provider state: duplicate managed resource address")
		}
		out[r.Address] = r
	}
	for _, child := range m.Children {
		if child == nil {
			return errors.New("provider state: null child module")
		}
		if err := collectProviderState(child, out); err != nil {
			return err
		}
	}
	return nil
}

type secretVariable struct {
	typ  cty.Type
	path string
}
type stateRenderer struct {
	vars  map[string]secretVariable
	quiet bool
}

func (r *stateRenderer) variables() []byte {
	f := hclwrite.NewEmptyFile()
	v := f.Body().AppendNewBlock("variable", []string{"pingone_environment_id"})
	v.Body().SetAttributeRaw("type", typeTokens(cty.String))
	for _, name := range sortedKeys(r.vars) {
		v := f.Body().AppendNewBlock("variable", []string{name})
		v.Body().SetAttributeRaw("type", typeTokens(r.vars[name].typ))
		v.Body().SetAttributeValue("sensitive", cty.True)
	}
	return f.Bytes()
}

func (r *stateRenderer) secret(path string, typ cty.Type) hclwrite.Tokens {
	base := safeVariableName(path)
	name := base
	if existing, ok := r.vars[name]; ok && existing.path != path {
		sum := sha256.Sum256([]byte(path))
		name = fmt.Sprintf("%s_%x", base, sum[:])
	}
	r.vars[name] = secretVariable{typ: typ, path: path}
	return hclwrite.TokensForTraversal(hclTraversal("var", name))
}

func omittedAttribute(a schemaAttribute) bool {
	// Optional+computed is configurable: for example webhook authentication
	// headers are sensitive, optional+computed map(string). Only computed-only
	// attributes represent generated outputs in the pinned provider schema.
	return a.Computed && !a.Optional && !a.Required
}

func (r *stateRenderer) body(body *hclwrite.Body, values map[string]any, sensitive any, schema schemaBlock, path string) error {
	for key := range values {
		if _, ok := schema.Attributes[key]; !ok {
			if _, ok := schema.Blocks[key]; !ok {
				return fmt.Errorf("provider state: %s has a field absent from the provider schema", path)
			}
		}
	}
	for _, key := range sortedKeys(schema.Attributes) {
		if !providerIdentifier.MatchString(key) {
			return errors.New("provider state: invalid schema attribute name")
		}
		a := schema.Attributes[key]
		tokens, emit, err := r.attribute(values[key], sensitiveChild(sensitive, key), a, path+"."+key)
		if err != nil {
			return fmt.Errorf("provider state: %s.%s: %w", path, key, err)
		}
		if emit {
			body.SetAttributeRaw(key, tokens)
		}
	}
	for _, name := range sortedKeys(schema.Blocks) {
		if !providerIdentifier.MatchString(name) {
			return errors.New("provider state: invalid schema block name")
		}
		n := schema.Blocks[name]
		v := values[name]
		items, keys, err := collectionItems(v, n.Nesting)
		if err != nil {
			return fmt.Errorf("provider state: %s.%s: %w", path, name, err)
		}
		if len(items) < n.MinItems || (n.MaxItems > 0 && len(items) > n.MaxItems) {
			return errors.New("provider state: nested block cardinality does not match schema")
		}
		for i, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return errors.New("provider state: nested block must be an object")
			}
			var labels []string
			var selector any = i
			itemPath := fmt.Sprintf("%s.%s[%d]", path, name, i)
			if n.Nesting == "map" {
				labels = []string{keys[i]}
				selector = keys[i]
				itemPath = fmt.Sprintf("%s.%s[%q]", path, name, keys[i])
			}
			marker := sensitiveChild(sensitive, name)
			if n.Nesting != "single" && n.Nesting != "group" {
				marker = sensitiveChild(marker, selector)
			}
			b := body.AppendNewBlock(name, labels)
			if err := r.body(b.Body(), m, marker, n.Block, itemPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *stateRenderer) attribute(value, sensitive any, a schemaAttribute, path string) (hclwrite.Tokens, bool, error) {
	if omittedAttribute(a) {
		return nil, false, nil
	}
	if !a.Optional && !a.Required {
		return nil, false, errors.New("attribute is not configurable")
	}
	typ, err := configuredType(a)
	if err != nil {
		return nil, false, err
	}
	if value == nil {
		if a.Required {
			return nil, false, errors.New("required field is missing or null")
		}
		return nil, false, nil
	}
	// Validate even secrets before replacing their value with a reference.
	secret := a.Sensitive || sensitive == true
	childRenderer := r
	if secret {
		copy := *r
		copy.quiet = true
		childRenderer = &copy
	}
	var tokens hclwrite.Tokens
	if a.Nested != nil {
		tokens, err = childRenderer.nestedAttribute(value, sensitive, a.Nested, path)
	} else {
		tokens, err = childRenderer.typedValue(value, sensitive, typ, path)
	}
	if err != nil {
		return nil, false, err
	}
	if strings.HasSuffix(path, ".environment_id") {
		if !typ.Equals(cty.String) {
			return nil, false, errors.New("environment_id must be a string")
		}
		return hclwrite.TokensForTraversal(hclTraversal("var", "pingone_environment_id")), true, nil
	}
	if secret && !r.quiet {
		tokens = r.secret(path, typ)
	}
	return tokens, true, nil
}

func (r *stateRenderer) nestedAttribute(value, sensitive any, n *nestedAttribute, path string) (hclwrite.Tokens, error) {
	items, keys, err := collectionItems(value, n.Nesting)
	if err != nil {
		return nil, err
	}
	parts := make([]hclwrite.Tokens, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("nested attribute must contain objects")
		}
		marker := sensitive
		itemPath := path
		if n.Nesting == "map" {
			marker = sensitiveChild(sensitive, keys[i])
			itemPath = fmt.Sprintf("%s[%q]", path, keys[i])
		} else if n.Nesting != "single" {
			marker = sensitiveChild(sensitive, i)
			itemPath = fmt.Sprintf("%s[%d]", path, i)
		}
		for key := range m {
			if _, ok := n.Attributes[key]; !ok {
				return nil, errors.New("nested field is absent from provider schema")
			}
		}
		var attrs []hclwrite.ObjectAttrTokens
		for _, key := range sortedKeys(n.Attributes) {
			tokens, emit, err := r.attribute(m[key], sensitiveChild(marker, key), n.Attributes[key], itemPath+"."+key)
			if err != nil {
				return nil, fmt.Errorf("nested attribute: %w", err)
			}
			if emit {
				attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(key)), Value: tokens})
			}
		}
		parts = append(parts, hclwrite.TokensForObject(attrs))
	}
	return collectionTokens(parts, keys, n.Nesting), nil
}

// typedValue handles legacy SDK object/collection types, where only show JSON
// can supply field-level sensitivity. Validate with cty, but retain state order
// when pairing set/list entries with Terraform's sensitivity mask.
func (r *stateRenderer) typedValue(value, sensitive any, typ cty.Type, path string) (hclwrite.Tokens, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("cannot encode configured value")
	}
	v, err := ctyjson.Unmarshal(raw, typ)
	if err != nil {
		return nil, errors.New("configured value does not match schema type")
	}
	if sensitive == true && !r.quiet {
		return r.secret(path, typ), nil
	}
	if value == nil {
		return hclwrite.TokensForValue(v), nil
	}
	if typ.IsObjectType() || typ.IsMapType() {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("expected object or map")
		}
		var attrs []hclwrite.ObjectAttrTokens
		for _, key := range sortedKeys(m) {
			var itemType cty.Type
			if typ.IsObjectType() {
				if !typ.HasAttribute(key) {
					return nil, errors.New("object field is absent from schema")
				}
				itemType = typ.AttributeType(key)
			} else {
				itemType = typ.ElementType()
			}
			tokens, err := r.typedValue(m[key], sensitiveChild(sensitive, key), itemType, fmt.Sprintf("%s[%q]", path, key))
			if err != nil {
				return nil, err
			}
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(key)), Value: tokens})
		}
		return hclwrite.TokensForObject(attrs), nil
	}
	if typ.IsListType() || typ.IsSetType() || typ.IsTupleType() {
		list, ok := value.([]any)
		if !ok {
			return nil, errors.New("expected collection")
		}
		parts := make([]hclwrite.Tokens, 0, len(list))
		for i, item := range list {
			var itemType cty.Type
			if typ.IsTupleType() {
				itemType = typ.TupleElementType(i)
			} else {
				itemType = typ.ElementType()
			}
			tokens, err := r.typedValue(item, sensitiveChild(sensitive, i), itemType, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			parts = append(parts, tokens)
		}
		return hclwrite.TokensForTuple(parts), nil
	}
	return hclwrite.TokensForValue(v), nil
}

func configuredType(a schemaAttribute) (cty.Type, error) {
	if a.Nested == nil {
		if len(a.Type) == 0 {
			return cty.NilType, errors.New("schema has no type")
		}
		typ, err := ctyjson.UnmarshalType(a.Type)
		if err != nil || !concreteType(typ) {
			return cty.NilType, errors.New("unsupported schema type")
		}
		return typ, nil
	}
	if len(a.Type) > 0 {
		return cty.NilType, errors.New("schema mixes type and nested_type")
	}
	attrs := map[string]cty.Type{}
	var optional []string
	for _, key := range sortedKeys(a.Nested.Attributes) {
		attr := a.Nested.Attributes[key]
		if omittedAttribute(attr) {
			continue
		}
		if !attr.Required && !attr.Optional {
			return cty.NilType, errors.New("nested attribute is not configurable")
		}
		typ, err := configuredType(attr)
		if err != nil {
			return cty.NilType, err
		}
		attrs[key] = typ
		if !attr.Required {
			optional = append(optional, key)
		}
	}
	obj := cty.ObjectWithOptionalAttrs(attrs, optional)
	switch a.Nested.Nesting {
	case "single":
		return obj, nil
	case "list":
		return cty.List(obj), nil
	case "set":
		return cty.Set(obj), nil
	case "map":
		return cty.Map(obj), nil
	default:
		return cty.NilType, errors.New("unsupported nested attribute nesting mode")
	}
}

func concreteType(t cty.Type) bool {
	if t == cty.NilType || t.Equals(cty.DynamicPseudoType) || t.IsCapsuleType() {
		return false
	}
	if t.IsPrimitiveType() {
		return true
	}
	if t.IsCollectionType() {
		return concreteType(t.ElementType())
	}
	if t.IsObjectType() {
		for _, a := range t.AttributeTypes() {
			if !concreteType(a) {
				return false
			}
		}
		return true
	}
	if t.IsTupleType() {
		for _, a := range t.TupleElementTypes() {
			if !concreteType(a) {
				return false
			}
		}
		return true
	}
	return false
}

func collectionItems(v any, nesting string) ([]any, []string, error) {
	if nesting != "single" && nesting != "group" && nesting != "list" && nesting != "set" && nesting != "map" {
		return nil, nil, errors.New("unsupported nesting mode")
	}
	if v == nil {
		return nil, nil, nil
	}
	switch nesting {
	case "single", "group":
		if _, ok := v.(map[string]any); !ok {
			return nil, nil, errors.New("expected nested object")
		}
		return []any{v}, nil, nil
	case "list", "set":
		items, ok := v.([]any)
		if !ok {
			return nil, nil, errors.New("expected nested collection")
		}
		return items, nil, nil
	default:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, nil, errors.New("expected nested map")
		}
		keys := sortedKeys(m)
		items := make([]any, 0, len(keys))
		for _, key := range keys {
			items = append(items, m[key])
		}
		return items, keys, nil
	}
}

func collectionTokens(parts []hclwrite.Tokens, keys []string, nesting string) hclwrite.Tokens {
	if nesting == "single" {
		return parts[0]
	}
	if nesting == "map" {
		attrs := make([]hclwrite.ObjectAttrTokens, 0, len(parts))
		for i, p := range parts {
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(keys[i])), Value: p})
		}
		return hclwrite.TokensForObject(attrs)
	}
	return hclwrite.TokensForTuple(parts)
}

func validateSensitivity(value, marker any) error {
	switch m := marker.(type) {
	case nil, bool:
		return nil
	case map[string]any:
		v, ok := value.(map[string]any)
		if !ok {
			return errors.New("invalid object sensitivity")
		}
		for k, s := range m {
			item, ok := v[k]
			if !ok {
				return errors.New("invalid sensitivity field")
			}
			if err := validateSensitivity(item, s); err != nil {
				return err
			}
		}
	case []any:
		v, ok := value.([]any)
		if !ok || (len(m) != 0 && len(v) != len(m)) {
			return errors.New("invalid collection sensitivity")
		}
		for i, s := range m {
			if err := validateSensitivity(v[i], s); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid sensitivity marker")
	}
	return nil
}

func sensitiveChild(marker, key any) any {
	if marker == true {
		return true
	}
	switch m := marker.(type) {
	case map[string]any:
		if k, ok := key.(string); ok {
			return m[k]
		}
	case []any:
		if i, ok := key.(int); ok && i >= 0 && i < len(m) {
			return m[i]
		}
	}
	return nil
}

func typeTokens(t cty.Type) hclwrite.Tokens {
	// These expressions contain only types and quoted schema field names.
	f, diags := hclwrite.ParseConfig([]byte("type = "+typeExpression(t)+"\n"), "type.tf", hcl.InitialPos)
	if diags.HasErrors() {
		panic("invalid internal type expression")
	}
	return f.Body().GetAttribute("type").Expr().BuildTokens(nil)
}

func typeExpression(t cty.Type) string {
	switch {
	case t.Equals(cty.String):
		return "string"
	case t.Equals(cty.Bool):
		return "bool"
	case t.Equals(cty.Number):
		return "number"
	case t.IsListType():
		return "list(" + typeExpression(t.ElementType()) + ")"
	case t.IsSetType():
		return "set(" + typeExpression(t.ElementType()) + ")"
	case t.IsMapType():
		return "map(" + typeExpression(t.ElementType()) + ")"
	case t.IsTupleType():
		var parts []string
		for _, a := range t.TupleElementTypes() {
			parts = append(parts, typeExpression(a))
		}
		return "tuple([" + strings.Join(parts, ",") + "])"
	case t.IsObjectType():
		var parts []string
		for _, key := range sortedKeys(t.AttributeTypes()) {
			expr := typeExpression(t.AttributeType(key))
			if t.AttributeOptional(key) {
				expr = "optional(" + expr + ")"
			}
			parts = append(parts, string(hclwrite.TokensForValue(cty.StringVal(key)).Bytes())+"="+expr)
		}
		return "object({" + strings.Join(parts, ",") + "})"
	default:
		panic("unsupported internal type")
	}
}

func hclTraversal(parts ...string) hcl.Traversal {
	out := make(hcl.Traversal, 0, len(parts))
	for i, p := range parts {
		if i == 0 {
			out = append(out, hcl.TraverseRoot{Name: p})
		} else {
			out = append(out, hcl.TraverseAttr{Name: p})
		}
	}
	return out
}

func safeVariableName(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9_]+`).ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "secret_" + s
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
