// Package davinci reads a read-only inventory of DaVinci configuration from
// the PingOne Platform DaVinci admin APIs: flows, connector instances,
// variables, applications and application flow policies.
//
// # Endpoints
//
// Paths, collection keys and field names were verified against
// github.com/pingidentity/pingone-go-client v0.13.0, the client that
// terraform-provider-pingone v1.23.0 uses (pingone/api_da_vinci_*.go and the
// model_da_vinci_*_response*.go files). They are relative to the PingOne API
// base, https://api.pingone.<tld>/v1:
//
//	GET /environments/{envID}/flows                                     _embedded.flows
//	GET /environments/{envID}/flows/{flowID}                            one flow, with graphData
//	GET /environments/{envID}/connectorInstances                        _embedded.connectorInstances
//	GET /environments/{envID}/variables                                 _embedded.variables (paged: _links.next)
//	GET /environments/{envID}/davinciApplications                       _embedded.davinciApplications
//	GET /environments/{envID}/davinciApplications/{appID}/flowPolicies  _embedded.flowPolicies
//
// # Assumptions (not verifiable offline; Phase 0 spike #61, #62)
//
//   - The flow list may omit graphData, so every flow is fetched again by ID.
//   - Only the variables collection declares a next link in the client
//     models; the reader still follows _links.next for every collection.
//   - Connector instance properties are a flat map of
//     {"<prop>": {"type": ..., "value": ...}}, and masked secrets come back as
//     "******". Whether unmasked secrets can ever come back is unknown, so the
//     translator never emits any connector property value it classifies as
//     secret.
//   - The DaVinci Admin Read Only role grants every GET above.
//
// Application client secrets and API key values are never decoded: the
// response types below have no fields for them.
package davinci

import "encoding/json"

// Kind names one inventory collection.
type Kind string

const (
	KindFlows        Kind = "davinci_flows"
	KindConnections  Kind = "davinci_connections"
	KindVariables    Kind = "davinci_variables"
	KindApplications Kind = "davinci_applications"
	KindFlowPolicies Kind = "davinci_flow_policies"
)

// Reason classifies why part of an inventory could not be read.
type Reason string

const (
	ReasonUnauthorized Reason = "unauthorized" // 401: token rejected or expired
	ReasonForbidden    Reason = "forbidden"    // 403: role or license does not allow the read
	ReasonNotFound     Reason = "not_found"    // 404: environment or resource missing, or API not enabled
	ReasonRateLimited  Reason = "rate_limited" // 429
	ReasonServer       Reason = "server_error" // 5xx
	ReasonHTTP         Reason = "http_error"   // other non-2xx
	ReasonTransport    Reason = "transport"    // no response
	ReasonMalformed    Reason = "malformed"    // response did not decode, or had invalid IDs
	ReasonIncomplete   Reason = "incomplete"   // pagination ended early or looped
	ReasonCredential   Reason = "credential"   // no token could be obtained
)

// Failure records one kind that is missing or incomplete. Count is how many
// items failed when the collection itself was listed (for example flows whose
// detail GET failed); 0 means the whole collection failed.
type Failure struct {
	Kind   Kind   `json:"kind"`
	Reason Reason `json:"reason"`
	Status int    `json:"status,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// Inventory is what was read from one environment. Partial lists every kind
// that is missing or incomplete; callers must not treat a missing item as
// deleted when its kind is partial.
type Inventory struct {
	EnvironmentID string              `json:"environment_id"`
	Flows         []Flow              `json:"flows"`
	Connections   []ConnectorInstance `json:"connector_instances"`
	Variables     []Variable          `json:"variables"`
	Applications  []Application       `json:"applications"`
	FlowPolicies  []FlowPolicy        `json:"flow_policies"`
	Partial       []Failure           `json:"partial,omitempty"`
}

// Complete reports whether kind was read in full.
func (i *Inventory) Complete(kind Kind) bool {
	for _, f := range i.Partial {
		if f.Kind == kind {
			return false
		}
	}
	return true
}

// Ref is a {"id": ...} relationship.
type Ref struct {
	ID string `json:"id"`
}

// XY is a position or pan offset.
type XY struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Flow is one DaVinci flow (DaVinciFlowResponse). Computed fields such as
// connectors, currentVersion, publishedVersion and linter counts are not
// decoded.
type Flow struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Description  *string                    `json:"description,omitempty"`
	Color        *string                    `json:"color,omitempty"`
	Enabled      *bool                      `json:"enabled,omitempty"`
	GraphData    *GraphData                 `json:"graphData,omitempty"`
	InputSchema  []InputField               `json:"inputSchema,omitempty"`
	OutputSchema *OutputSchema              `json:"outputSchema,omitempty"`
	Settings     map[string]json.RawMessage `json:"settings,omitempty"`
	Trigger      *Trigger                   `json:"trigger,omitempty"`
}

// GraphData is the flow canvas.
type GraphData struct {
	BoxSelectionEnabled *bool           `json:"boxSelectionEnabled,omitempty"`
	Elements            Elements        `json:"elements"`
	Pan                 *XY             `json:"pan,omitempty"`
	PanningEnabled      *bool           `json:"panningEnabled,omitempty"`
	UserPanningEnabled  *bool           `json:"userPanningEnabled,omitempty"`
	UserZoomingEnabled  *bool           `json:"userZoomingEnabled,omitempty"`
	Zoom                *float64        `json:"zoom,omitempty"`
	MaxZoom             *float64        `json:"maxZoom,omitempty"`
	MinZoom             *float64        `json:"minZoom,omitempty"`
	ZoomingEnabled      *bool           `json:"zoomingEnabled,omitempty"`
	Renderer            json.RawMessage `json:"renderer,omitempty"`
	Data                json.RawMessage `json:"data,omitempty"`
}

// Elements holds nodes and edges as lists (the API shape).
type Elements struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges,omitempty"`
}

// Node is one graph node.
type Node struct {
	Data       NodeData `json:"data"`
	Position   *XY      `json:"position,omitempty"`
	Group      *string  `json:"group,omitempty"`
	Classes    *string  `json:"classes,omitempty"`
	Grabbable  *bool    `json:"grabbable,omitempty"`
	Locked     *bool    `json:"locked,omitempty"`
	Pannable   *bool    `json:"pannable,omitempty"`
	Removed    *bool    `json:"removed,omitempty"`
	Selectable *bool    `json:"selectable,omitempty"`
	Selected   *bool    `json:"selected,omitempty"`
}

// NodeData is a node's data. name, idUnique and linterError are computed and
// not decoded.
type NodeData struct {
	ID              string          `json:"id"`
	NodeType        string          `json:"nodeType"`
	CapabilityClass *string         `json:"capabilityClass,omitempty"`
	CapabilityName  *string         `json:"capabilityName,omitempty"`
	ConnectionID    *string         `json:"connectionId,omitempty"`
	ConnectorID     *string         `json:"connectorId,omitempty"`
	Label           *string         `json:"label,omitempty"`
	Status          *string         `json:"status,omitempty"`
	Type            *string         `json:"type,omitempty"`
	Outcomes        []Outcome       `json:"outcomes,omitempty"`
	Properties      json.RawMessage `json:"properties,omitempty"`
}

// Outcome is a node outcome.
type Outcome struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Result string `json:"result"`
}

// Edge is one graph edge.
type Edge struct {
	Data       EdgeData `json:"data"`
	Position   *XY      `json:"position,omitempty"`
	Group      *string  `json:"group,omitempty"`
	Classes    *string  `json:"classes,omitempty"`
	Grabbable  *bool    `json:"grabbable,omitempty"`
	Locked     *bool    `json:"locked,omitempty"`
	Pannable   *bool    `json:"pannable,omitempty"`
	Removed    *bool    `json:"removed,omitempty"`
	Selectable *bool    `json:"selectable,omitempty"`
	Selected   *bool    `json:"selected,omitempty"`
}

// EdgeData is an edge's data.
type EdgeData struct {
	ID                 string  `json:"id"`
	Source             string  `json:"source"`
	Target             string  `json:"target"`
	MultiValueSourceID *string `json:"multiValueSourceId,omitempty"`
}

// InputField is one inputSchema entry.
type InputField struct {
	PropertyName         string  `json:"propertyName"`
	PreferredControlType string  `json:"preferredControlType"`
	PreferredDataType    string  `json:"preferredDataType"`
	Description          *string `json:"description,omitempty"`
	IsExpanded           *bool   `json:"isExpanded,omitempty"`
	Required             *bool   `json:"required,omitempty"`
}

// OutputSchema is a flow's outputSchema.
type OutputSchema struct {
	Output struct {
		Type                 string          `json:"type"`
		Properties           json.RawMessage `json:"properties,omitempty"`
		AdditionalProperties *bool           `json:"additionalProperties,omitempty"`
	} `json:"output"`
}

// Trigger is a flow or policy trigger.
type Trigger struct {
	Type          *string        `json:"type,omitempty"`
	Subtype       *string        `json:"subtype,omitempty"`
	Configuration *TriggerConfig `json:"configuration,omitempty"`
}

// TriggerConfig holds the MFA and password timers.
type TriggerConfig struct {
	MFA TimedToggle `json:"mfa"`
	PWD TimedToggle `json:"pwd"`
}

// TimedToggle is an enabled flag with a duration.
type TimedToggle struct {
	Enabled    bool    `json:"enabled"`
	Time       float64 `json:"time"`
	TimeFormat string  `json:"timeFormat"`
}

// ConnectorInstance is one connection. Properties is the raw flat property
// map and may contain masked values.
type ConnectorInstance struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Connector  Ref             `json:"connector"`
	Properties json.RawMessage `json:"properties,omitempty"`
}

// Variable is one DaVinci variable. Value is raw because it is a union of
// string, number, boolean and object.
type Variable struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	DataType    string          `json:"dataType"`
	Context     *string         `json:"context,omitempty"`
	DisplayName *string         `json:"displayName,omitempty"`
	Flow        *Ref            `json:"flow,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Mutable     *bool           `json:"mutable,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
}

// Application is one DaVinci application without its client secret or API
// key value.
type Application struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	APIKey *struct {
		Enabled bool `json:"enabled"`
	} `json:"apiKey,omitempty"`
	OAuth *ApplicationOAuth `json:"oauth,omitempty"`
}

// ApplicationOAuth is the non-secret OAuth configuration.
type ApplicationOAuth struct {
	EnforceSignedRequestOpenid *bool    `json:"enforceSignedRequestOpenid,omitempty"`
	GrantTypes                 []string `json:"grantTypes,omitempty"`
	LogoutURIs                 []string `json:"logoutUris,omitempty"`
	RedirectURIs               []string `json:"redirectUris,omitempty"`
	Scopes                     []string `json:"scopes,omitempty"`
	SPJWKSOpenid               *string  `json:"spJwksOpenid,omitempty"`
	SPJWKSURL                  *string  `json:"spjwksUrl,omitempty"`
}

// FlowPolicy is one application flow policy. ApplicationID is set by the
// reader from the request path.
type FlowPolicy struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Status            string         `json:"status"`
	ApplicationID     string         `json:"-"`
	FlowDistributions []Distribution `json:"flowDistributions"`
	Trigger           *Trigger       `json:"trigger,omitempty"`
}

// Distribution is a weighted route to a flow version.
type Distribution struct {
	ID           string   `json:"id"`
	Version      float64  `json:"version"`
	Weight       *float64 `json:"weight,omitempty"`
	IP           []string `json:"ip,omitempty"`
	SuccessNodes []Ref    `json:"successNodes,omitempty"`
}
