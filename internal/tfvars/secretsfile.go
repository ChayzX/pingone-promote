package tfvars

import (
	"errors"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// SecretsFileHeader is the managed first line of a generated secrets.tf.
const SecretsFileHeader = "# Managed by pingone-promote. One variable per PingOne secret; the pipeline sets TF_VAR_<name>."

// rootVariableNames are the root stack variables a secret variable must not
// shadow, plus names Terraform reserves for variables.
var rootVariableNames = map[string]bool{
	"tfvars_schema_version": true, "pingone_environment_id": true, "parameters": true,
	"davinci_connections": true, "davinci_variables": true, "davinci_flows": true,
	"davinci_applications": true, "import_ids": true, "pingone_secrets": true,
	"source": true, "version": true, "providers": true, "count": true, "for_each": true,
	"lifecycle": true, "depends_on": true, "locals": true,
}

// SecretsFileError names a key that cannot become a root variable.
type SecretsFileError struct {
	Key    string
	Reason string
}

func (e *SecretsFileError) Error() string { return "secrets.tf: " + e.Key + ": " + e.Reason }

// WriteSecretsTF renders the root stack's secrets.tf for the given secret
// keys. GitLab CI injects each secret as its own TF_VAR_<key> environment
// variable, and Terraform ignores TF_VAR_ values for undeclared variables, so
// every secret is declared individually:
//
//	variable "<key>" { type = string, sensitive = true, default = null }
//
// and collected into local.pingone_secrets for the module's pingone_secrets
// input. A secret left unset stays null, which the module contract reports by
// name. Output is sorted, de-duplicated and formatted like terraform fmt.
func WriteSecretsTF(secretKeys []string) ([]byte, error) {
	keys := sortedUnique(secretKeys)
	for _, k := range keys {
		switch {
		case !IsSecretKey(k):
			return nil, &SecretsFileError{Key: k, Reason: "must match " + SecretKeyPattern}
		case rootVariableNames[k]:
			return nil, &SecretsFileError{Key: k, Reason: "collides with a root stack or reserved variable name; choose another secret name"}
		}
	}
	var b strings.Builder
	b.WriteString(SecretsFileHeader + "\n")
	for _, k := range keys {
		b.WriteString("\nvariable \"" + k + "\" {\n")
		b.WriteString("  description = \"PingOne secret " + k + " (pipeline: TF_VAR_" + k + ").\"\n")
		b.WriteString("  type = string\n  sensitive = true\n  default = null\n}\n")
	}
	b.WriteString("\nlocals {\n")
	if len(keys) == 0 {
		b.WriteString("  pingone_secrets = {}\n")
	} else {
		b.WriteString("  pingone_secrets = {\n")
		for _, k := range keys {
			b.WriteString("    \"" + k + "\" = var." + k + "\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	out := hclwrite.Format([]byte(b.String()))
	if _, diags := hclwrite.ParseConfig(out, "secrets.tf", hcl.InitialPos); diags.HasErrors() {
		return nil, errors.New("secrets.tf: generated HCL does not parse")
	}
	return out, nil
}

// SecretKeysOf returns the sorted secret keys a document requires.
func SecretKeysOf(doc *Document) []string {
	r := Validate(doc)
	out := append([]string(nil), r.RequiredSecrets...)
	sort.Strings(out)
	return out
}
