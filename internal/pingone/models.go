package pingone

import "encoding/json"

type Profile struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Region            string `json:"region"`
	EnvironmentID     string `json:"environmentId"`
	AuthEnvironmentID string `json:"authEnvironmentId"`
	ClientID          string `json:"clientId"`
	SecretMode        string `json:"secretMode"`
	SecretEnv         string `json:"secretEnv,omitempty"`
}
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}
type Resource struct {
	ID            string         `json:"id"`
	URL           string         `json:"url"`
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	Configuration map[string]any `json:"configuration,omitempty"`
	Dependencies  []string       `json:"dependencies,omitempty"`
}
type Inventory struct {
	APIOrigin      string     `json:"apiOrigin"`
	EnvironmentID  string     `json:"environmentId"`
	OrganizationID string     `json:"organizationId"`
	SnapshotID     string     `json:"snapshotId"`
	Resources      []Resource `json:"resources"`
}
type Snapshot struct {
	ID            string          `json:"id"`
	SnapshotID    string          `json:"snapshotId"`
	Resource      Ref             `json:"resource"`
	URL           string          `json:"resourceUrl"`
	Status        string          `json:"status"`
	Configuration map[string]any  `json:"configuration"`
	Dependencies  []string        `json:"referencedResources"`
	Error         json.RawMessage `json:"error"`
}
type Promotion struct {
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	Source         Ref               `json:"sourceEnvironment"`
	Target         Ref               `json:"targetEnvironment"`
	SourceSnapshot Ref               `json:"sourceSnapshot"`
	TargetSnapshot Ref               `json:"targetSnapshot"`
	Previous       Ref               `json:"previousPromotion"`
	Mapping        map[string]string `json:"resourceMapping"`
	Errors         json.RawMessage   `json:"errors,omitempty"`
}
type Step struct {
	Action  string          `json:"action"`
	URL     string          `json:"resourceUrl"`
	Source  Ref             `json:"sourceResource"`
	Target  Ref             `json:"targetResource"`
	Payload json.RawMessage `json:"payload"`
}

const ExecuteType = "application/vnd.pingidentity.promotion.execute+json"
const RollbackType = "application/vnd.pingidentity.promotion.rollback+json"

var RegionDomains = map[string]string{"us": "pingone.com", "eu": "pingone.eu", "ca": "pingone.ca", "au": "pingone.com.au", "sg": "pingone.sg", "asia": "pingone.asia"}
