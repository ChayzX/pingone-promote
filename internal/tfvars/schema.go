// Package tfvars reads, writes, validates and diffs the per-environment
// pingone-promote Terraform variable files (tfvars schema v1).
//
// The Go types in this file are the single source of truth for schema v1 on
// the Go side. terraform/modules/pingone-davinci/variables.tf mirrors them and
// the module tests consume tfvars written by this package, so both sides agree
// on one schema. Field order in each struct is the order the writer emits.
//
// Struct tags use the form `tf:"name[,optional][,json][,set]"`:
//   - optional: the attribute may be absent (pointer, slice or map field);
//   - json: the string holds a JSON object written as a canonical heredoc;
//   - set: a list of strings with set semantics, written sorted and unique.
//
// Errors from this package never include attribute values, only paths and
// positions, so they are safe to log and to show in merge request text.
package tfvars

// SchemaVersion is the only tfvars schema version this package accepts.
const SchemaVersion = 1

// Header is the managed first line of every written file.
const Header = "# Managed by pingone-promote (tfvars schema v1). Comments outside this header are not preserved."

// Document is one environment's tfvars file.
type Document struct {
	SchemaVersion int                    `tf:"tfvars_schema_version"`
	EnvironmentID string                 `tf:"pingone_environment_id"`
	Parameters    map[string]string      `tf:"parameters"`
	Connections   map[string]Connection  `tf:"davinci_connections"`
	Variables     map[string]Variable    `tf:"davinci_variables"`
	Flows         map[string]Flow        `tf:"davinci_flows"`
	Applications  map[string]Application `tf:"davinci_applications"`
	ImportIDs     ImportIDs              `tf:"import_ids"`
}

// Connection is a DaVinci connector instance.
type Connection struct {
	Name       string  `tf:"name"`
	Connector  string  `tf:"connector"`
	Properties *string `tf:"properties,optional,json"`
}

// Variable is a DaVinci variable. Flow is the logical key of the owning flow
// when Context is "flow". Secret is the pingone_secrets key that supplies the
// value when DataType is "secret"; Value must then be absent.
type Variable struct {
	Name        string         `tf:"name"`
	Context     string         `tf:"context"`
	DataType    string         `tf:"data_type"`
	Mutable     bool           `tf:"mutable"`
	DisplayName *string        `tf:"display_name,optional"`
	Flow        *string        `tf:"flow,optional"`
	Min         *float64       `tf:"min,optional"`
	Max         *float64       `tf:"max,optional"`
	Secret      *string        `tf:"secret,optional"`
	Value       *VariableValue `tf:"value,optional"`
}

// VariableValue holds at most one typed value.
type VariableValue struct {
	String     *string  `tf:"string,optional"`
	Bool       *bool    `tf:"bool,optional"`
	Float32    *float64 `tf:"float32,optional"`
	JSONObject *string  `tf:"json_object,optional,json"`
}

// Flow is a DaVinci flow. Tier (0..3) orders subflow references: a flow may
// reference only flows in lower tiers.
type Flow struct {
	Name         string        `tf:"name"`
	Description  *string       `tf:"description,optional"`
	Color        *string       `tf:"color,optional"`
	Tier         int           `tf:"tier"`
	Enabled      *bool         `tf:"enabled,optional"`
	Deploy       *bool         `tf:"deploy,optional"`
	Settings     *FlowSettings `tf:"settings,optional"`
	Trigger      *FlowTrigger  `tf:"trigger,optional"`
	InputSchema  []InputField  `tf:"input_schema,optional"`
	OutputSchema *OutputSchema `tf:"output_schema,optional"`
	Graph        Graph         `tf:"graph"`
}

// FlowSettings mirrors pingone_davinci_flow.settings.
type FlowSettings struct {
	CSP                             *string  `tf:"csp,optional"`
	CSS                             *string  `tf:"css,optional"`
	CSSLinks                        []string `tf:"css_links,optional,set"`
	CustomErrorScreenBrandLogoURL   *string  `tf:"custom_error_screen_brand_logo_url,optional"`
	CustomErrorShowFooter           *bool    `tf:"custom_error_show_footer,optional"`
	CustomFaviconLink               *string  `tf:"custom_favicon_link,optional"`
	CustomLogoURLSelection          *float64 `tf:"custom_logo_urlselection,optional"`
	CustomTimeoutErrorScreenCSS     *string  `tf:"custom_timeout_error_screen_css,optional"`
	CustomTimeoutErrorScreenHTML    *string  `tf:"custom_timeout_error_screen_html,optional"`
	CustomTimeoutErrorScreenMessage *string  `tf:"custom_timeout_error_screen_message,optional"`
	CustomTitle                     *string  `tf:"custom_title,optional"`
	DefaultErrorScreenBrandLogo     *bool    `tf:"default_error_screen_brand_logo,optional"`
	FlowHTTPTimeoutInSeconds        *float64 `tf:"flow_http_timeout_in_seconds,optional"`
	FlowTimeoutInSeconds            *float64 `tf:"flow_timeout_in_seconds,optional"`
	IntermediateLoadingScreenCSS    *string  `tf:"intermediate_loading_screen_css,optional"`
	IntermediateLoadingScreenHTML   *string  `tf:"intermediate_loading_screen_html,optional"`
	JSCustomFlowPlayer              *string  `tf:"js_custom_flow_player,optional"`
	JSLinks                         []JSLink `tf:"js_links,optional"`
	LogLevel                        *int     `tf:"log_level,optional"`
	PreviewFormRenderingUpdates     *bool    `tf:"preview_form_rendering_updates,optional"`
	RequireAuthenticationToInitiate *bool    `tf:"require_authentication_to_initiate,optional"`
	ScrubSensitiveInfo              *bool    `tf:"scrub_sensitive_info,optional"`
	SensitiveInfoFields             []string `tf:"sensitive_info_fields,optional,set"`
	UseCSP                          *bool    `tf:"use_csp,optional"`
	UseCustomCSS                    *bool    `tf:"use_custom_css,optional"`
	UseCustomFlowPlayer             *bool    `tf:"use_custom_flow_player,optional"`
	UseCustomScript                 *bool    `tf:"use_custom_script,optional"`
	UseCustomTimeoutErrorScreen     *bool    `tf:"use_custom_timeout_error_screen,optional"`
	UseIntermediateLoadingScreen    *bool    `tf:"use_intermediate_loading_screen,optional"`
	ValidateOnSave                  *bool    `tf:"validate_on_save,optional"`
}

// JSLink mirrors pingone_davinci_flow.settings.js_links (all required).
type JSLink struct {
	Crossorigin    string `tf:"crossorigin"`
	Defer          bool   `tf:"defer"`
	Integrity      string `tf:"integrity"`
	Label          string `tf:"label"`
	Referrerpolicy string `tf:"referrerpolicy"`
	Type           string `tf:"type"`
	Value          string `tf:"value"`
}

// FlowTrigger mirrors pingone_davinci_flow.trigger.
type FlowTrigger struct {
	Type          string              `tf:"type"`
	Subtype       *string             `tf:"subtype,optional"`
	Configuration *TriggerConfigFixed `tf:"configuration,optional"`
}

// TriggerConfigFixed is the flow trigger configuration; mfa and pwd are
// required by the provider when configuration is set.
type TriggerConfigFixed struct {
	MFA TimedToggle `tf:"mfa"`
	PWD TimedToggle `tf:"pwd"`
}

// TimedToggle is an enabled flag with a duration.
type TimedToggle struct {
	Enabled    bool    `tf:"enabled"`
	Time       float64 `tf:"time"`
	TimeFormat string  `tf:"time_format"`
}

// InputField mirrors one pingone_davinci_flow.input_schema element.
type InputField struct {
	Description          *string `tf:"description,optional"`
	IsExpanded           *bool   `tf:"is_expanded,optional"`
	PreferredControlType *string `tf:"preferred_control_type,optional"`
	PreferredDataType    *string `tf:"preferred_data_type,optional"`
	PropertyName         *string `tf:"property_name,optional"`
	Required             *bool   `tf:"required,optional"`
}

// OutputSchema mirrors pingone_davinci_flow.output_schema.
type OutputSchema struct {
	Output OutputSpec `tf:"output"`
}

// OutputSpec mirrors pingone_davinci_flow.output_schema.output.
type OutputSpec struct {
	Type                 string  `tf:"type"`
	Properties           *string `tf:"properties,optional,json"`
	AdditionalProperties *bool   `tf:"additional_properties,optional"`
}

// Graph is pingone_davinci_flow.graph_data with nodes and edges lifted out of
// graph_data.elements. Node and edge map keys are DaVinci element IDs.
type Graph struct {
	BoxSelectionEnabled *bool           `tf:"box_selection_enabled,optional"`
	Data                *string         `tf:"data,optional"`
	MaxZoom             *float64        `tf:"max_zoom,optional"`
	MinZoom             *float64        `tf:"min_zoom,optional"`
	Pan                 *Position       `tf:"pan,optional"`
	PanningEnabled      *bool           `tf:"panning_enabled,optional"`
	Renderer            *string         `tf:"renderer,optional"`
	UserPanningEnabled  *bool           `tf:"user_panning_enabled,optional"`
	UserZoomingEnabled  *bool           `tf:"user_zooming_enabled,optional"`
	Zoom                *float64        `tf:"zoom,optional"`
	ZoomingEnabled      *bool           `tf:"zooming_enabled,optional"`
	Nodes               map[string]Node `tf:"nodes"`
	Edges               map[string]Edge `tf:"edges"`
}

// Position is an x/y coordinate.
type Position struct {
	X float64 `tf:"x"`
	Y float64 `tf:"y"`
}

// Node is one graph node. Connection is the logical key of a
// davinci_connections entry; the module resolves it to connection_id.
type Node struct {
	NodeType        string    `tf:"node_type"`
	Connection      *string   `tf:"connection,optional"`
	ConnectorID     *string   `tf:"connector_id,optional"`
	CapabilityName  *string   `tf:"capability_name,optional"`
	CapabilityClass *string   `tf:"capability_class,optional"`
	Label           *string   `tf:"label,optional"`
	Status          *string   `tf:"status,optional"`
	Type            *string   `tf:"type,optional"`
	Properties      *string   `tf:"properties,optional,json"`
	Outcomes        []Outcome `tf:"outcomes,optional"`
	Position        *Position `tf:"position,optional"`
	Classes         *string   `tf:"classes,optional"`
	Group           *string   `tf:"group,optional"`
	Grabbable       *bool     `tf:"grabbable,optional"`
	Locked          *bool     `tf:"locked,optional"`
	Pannable        *bool     `tf:"pannable,optional"`
	Removed         *bool     `tf:"removed,optional"`
	Selectable      *bool     `tf:"selectable,optional"`
	Selected        *bool     `tf:"selected,optional"`
}

// Outcome mirrors graph node data.outcomes.
type Outcome struct {
	ID     string `tf:"id"`
	Label  string `tf:"label"`
	Result string `tf:"result"`
}

// Edge is one graph edge between node IDs.
type Edge struct {
	Source             string    `tf:"source"`
	Target             string    `tf:"target"`
	MultiValueSourceID *string   `tf:"multi_value_source_id,optional"`
	Position           *Position `tf:"position,optional"`
	Classes            *string   `tf:"classes,optional"`
	Group              *string   `tf:"group,optional"`
	Grabbable          *bool     `tf:"grabbable,optional"`
	Locked             *bool     `tf:"locked,optional"`
	Pannable           *bool     `tf:"pannable,optional"`
	Removed            *bool     `tf:"removed,optional"`
	Selectable         *bool     `tf:"selectable,optional"`
	Selected           *bool     `tf:"selected,optional"`
}

// Application is a DaVinci application with its flow policies.
type Application struct {
	Name          string                `tf:"name"`
	APIKeyEnabled *bool                 `tf:"api_key_enabled,optional"`
	OAuth         *OAuth                `tf:"oauth,optional"`
	FlowPolicies  map[string]FlowPolicy `tf:"flow_policies,optional"`
}

// OAuth mirrors pingone_davinci_application.oauth (client_secret is computed
// and never authored).
type OAuth struct {
	EnforceSignedRequestOpenID *bool    `tf:"enforce_signed_request_openid,optional"`
	GrantTypes                 []string `tf:"grant_types,optional,set"`
	LogoutURIs                 []string `tf:"logout_uris,optional,set"`
	RedirectURIs               []string `tf:"redirect_uris,optional,set"`
	Scopes                     []string `tf:"scopes,optional,set"`
	SPJWKSOpenID               *string  `tf:"sp_jwks_openid,optional"`
	SPJWKSURL                  *string  `tf:"sp_jwks_url,optional"`
}

// FlowPolicy is a pingone_davinci_application_flow_policy.
type FlowPolicy struct {
	Name          string         `tf:"name"`
	Status        *string        `tf:"status,optional"`
	Trigger       *PolicyTrigger `tf:"trigger,optional"`
	Distributions []Distribution `tf:"distributions"`
}

// PolicyTrigger mirrors the flow policy trigger (all optional).
type PolicyTrigger struct {
	Type          *string              `tf:"type,optional"`
	Configuration *PolicyTriggerConfig `tf:"configuration,optional"`
}

// PolicyTriggerConfig mirrors the flow policy trigger configuration.
type PolicyTriggerConfig struct {
	MFA *OptionalTimedToggle `tf:"mfa,optional"`
	PWD *OptionalTimedToggle `tf:"pwd,optional"`
}

// OptionalTimedToggle is TimedToggle with optional fields.
type OptionalTimedToggle struct {
	Enabled    *bool    `tf:"enabled,optional"`
	Time       *float64 `tf:"time,optional"`
	TimeFormat *string  `tf:"time_format,optional"`
}

// Distribution routes a share of policy traffic to a flow by logical key.
type Distribution struct {
	Flow         string   `tf:"flow"`
	Version      float64  `tf:"version"`
	Weight       *float64 `tf:"weight,optional"`
	IP           []string `tf:"ip,optional,set"`
	SuccessNodes []string `tf:"success_nodes,optional,set"`
}

// ImportIDs maps logical keys to existing PingOne IDs for adoption. The
// module turns each entry into a for_each import block.
type ImportIDs struct {
	Connections  map[string]string `tf:"davinci_connections"`
	Flows        map[string]string `tf:"davinci_flows"`
	Variables    map[string]string `tf:"davinci_variables"`
	Applications map[string]string `tf:"davinci_applications"`
	FlowPolicies map[string]string `tf:"davinci_flow_policies"`
}
