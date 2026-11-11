package neutralize

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

func format(s string) []byte { return bytes.TrimSpace(hclwrite.Format([]byte(s))) }

const envIDValidation = `
  validation {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.pingone_environment_id))
    error_message = "The PingOne Environment ID must be a valid PingOne resource ID (UUID format)."
  }
`

// ChildVariableBlock declares v in the shared definitions (child module),
// in terraformer's style.
func ChildVariableBlock(v *Variable) []byte {
	var b strings.Builder
	b.WriteString("variable " + strconv.Quote(v.Name) + " {\n")
	b.WriteString("  type        = " + v.Type + "\n")
	b.WriteString("  description = " + strconv.Quote(v.Description) + "\n")
	if v.Kind == KindSecret {
		b.WriteString("  sensitive   = true\n")
	}
	if v.Kind == KindEnvironmentID {
		b.WriteString(envIDValidation)
	}
	b.WriteString("}\n")
	return format(b.String())
}

// RootVariableBlock declares an environment-local root variable. It has no
// default, so every environment overlay must set it (check enforces this).
func RootVariableBlock(v *Variable) []byte {
	var b strings.Builder
	b.WriteString("variable " + strconv.Quote(v.Name) + " {\n")
	b.WriteString("  type        = " + v.Type + "\n")
	b.WriteString("  description = " + strconv.Quote(v.Description) + "\n")
	if v.Kind == KindEnvironmentID {
		b.WriteString(envIDValidation)
	}
	b.WriteString("}\n")
	return format(b.String())
}

// SecretVariableBlock declares a pipeline secret in secrets.tf. The
// pipeline sets TF_VAR_<name> from customer_secrets.yml (ADR 0009).
func SecretVariableBlock(name string) []byte {
	return format("variable " + strconv.Quote(name) + " {\n" +
		"  description = " + strconv.Quote("PingOne secret "+name+" (pipeline: TF_VAR_"+name+").") + "\n" +
		"  type        = string\n  sensitive   = true\n  nullable    = false\n}\n")
}

// ImportIDsVariableBlock declares var.import_ids (ADR 0016).
func ImportIDsVariableBlock() []byte {
	return format(`variable "import_ids" {
  description = "Existing PingOne IDs to adopt in this environment, by module address (module.<name>.<type>.<label>). The environment ID prefix is added automatically."
  type        = map(string)
  default     = {}
  nullable    = false
}
`)
}

// ImportBlock is the conditional import for one static address (ADR 0016,
// spike S1): it imports only when var.import_ids has an entry for it.
func ImportBlock(moduleName, addr string) []byte {
	full := "module." + moduleName + "." + addr
	return format("import {\n" +
		"  for_each = { for k, v in var.import_ids : k => v if k == " + strconv.Quote(full) + " }\n" +
		"  to       = " + full + "\n" +
		"  id       = \"${var." + EnvironmentIDVariable + "}/${each.value}\"\n}\n")
}

// SingletonImportBlock adopts a resource that exists once per environment
// (settings): its import ID is the environment ID itself, so the block is
// the same in every environment and needs no import_ids entry. Importing an
// address that is already in state is a no-op.
func SingletonImportBlock(moduleName, addr string) []byte {
	full := "module." + moduleName + "." + addr
	return format("import {\n  to = " + full + "\n  id = var." + EnvironmentIDVariable + "\n}\n")
}

// ModuleInput is one "<name> = var.<name>" line of the module block.
func ModuleInput(name string) string { return "  " + name + " = var." + name + "\n" }

// ModuleBlock is the root module call for a new repository.
func ModuleBlock(moduleName, moduleDir string, inputs []string) []byte {
	var b strings.Builder
	b.WriteString("module " + strconv.Quote(moduleName) + " {\n")
	b.WriteString("  source = " + strconv.Quote("./"+moduleDir) + "\n\n")
	b.WriteString(ModuleInput(EnvironmentIDVariable))
	for _, in := range inputs {
		if in != EnvironmentIDVariable {
			b.WriteString(ModuleInput(in))
		}
	}
	b.WriteString("}\n")
	return format(b.String())
}

// ChildVersionsTF is definitions/versions.tf for a new repository,
// matching terraformer's (internal/module/generator.go generateVersionsTF).
const ChildVersionsTF = `terraform {
  required_version = ">= 1.5"

  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = ">= 1.19.1, < 2.0.0"
    }
  }
}
`
