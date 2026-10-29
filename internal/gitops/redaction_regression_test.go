package gitops

import (
	"bytes"
	"testing"
)

func TestRequestExportRedactsCredentialShapesAndRejectsUnredactedImports(t *testing.T) {
	const canary = "SYNTHETIC-EXPORT-CREDENTIAL"
	for name, secret := range map[string]map[string]any{
		"typed secret":      {"dataType": "secret", "value": canary},
		"sensitivity flag":  {"IsSensitive": true, "value": canary},
		"passphrase":        {"passphrase": canary},
		"connection string": {"connectionString": canary},
		"authorization":     {"authorization": canary},
		"DSN":               {"dsn": canary},
		"connection URI":    {"connectionUri": canary},
	} {
		t.Run(name, func(t *testing.T) {
			s, d := testInventories("https://api.pingone.com")
			s.Resources[0].Configuration = map[string]any{"name": "Flow", "settings": secret}
			r, err := NewRequest(s, d, "us", "us", []string{"flow"}, nil, "review")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(canary)) {
				t.Fatal("credential entered request.json export")
			}
			// A caller cannot bypass redaction by recomputing the public fingerprint.
			r.Configurations["flow"] = s.Resources[0].Configuration
			r.Fingerprints["flow"] = fingerprint(r.Configurations["flow"])
			if err := Validate(r); err == nil {
				t.Fatal("unredacted import accepted with matching fingerprint")
			}
		})
	}
}
