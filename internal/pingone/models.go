package pingone

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

var RegionDomains = map[string]string{"us": "pingone.com", "eu": "pingone.eu", "ca": "pingone.ca", "au": "pingone.com.au", "sg": "pingone.sg", "asia": "pingone.asia"}
