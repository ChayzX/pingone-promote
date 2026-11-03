package tfvars

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const minimalHead = `tfvars_schema_version  = 1
pingone_environment_id = "00000000-0000-4000-8000-000000000001"
parameters             = {}
davinci_connections    = {}
davinci_variables      = {}
davinci_applications   = {}
import_ids = {
  davinci_connections   = {}
  davinci_flows         = {}
  davinci_variables     = {}
  davinci_applications  = {}
  davinci_flow_policies = {}
}
`

func withFlows(flows string) string { return minimalHead + "davinci_flows = " + flows + "\n" }

func TestParseRejectsNonLiterals(t *testing.T) {
	const canary = "CANARYsecretVALUE"
	cases := map[string]string{
		"variable reference":    `{ f = { name = var.x, tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"function call":         `{ f = { name = upper("` + canary + `"), tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"interpolation":         `{ f = { name = "${"` + canary + `"}", tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"template directive":    `{ f = { name = "%{if true}` + canary + `%{endif}", tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"for expression":        `{ for k in ["a"] : k => { name = "x", tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"conditional":           `{ f = { name = true ? "a" : "b", tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"arithmetic":            `{ f = { name = "x", tier = 1 + 1, graph = { nodes = {}, edges = {} } } }`,
		"parentheses":           `{ f = { name = ("x"), tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"computed key":          `{ (var.k) = { name = "x", tier = 0, graph = { nodes = {}, edges = {} } } }`,
		"heredoc interpolation": "{ f = { name = <<-EOT\n    ${local.x}\n  EOT\n  tier = 0, graph = { nodes = {}, edges = {} } } }",
		"negated bool":          `{ f = { name = "x", tier = 0, enabled = !true, graph = { nodes = {}, edges = {} } } }`,
	}
	for name, flows := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(withFlows(flows)), "env.tfvars")
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("accepted non-literal: %v", err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked input value: %v", err)
			}
		})
	}
}

func TestParseStrictSchema(t *testing.T) {
	okFlow := `{ f = { name = "x", tier = 0, graph = { nodes = {}, edges = {} } } }`
	if _, err := Parse([]byte(withFlows(okFlow)), "env.tfvars"); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	cases := map[string]string{
		"unknown nested attribute": withFlows(`{ f = { name = "x", tier = 0, colour = "red", graph = { nodes = {}, edges = {} } } }`),
		"unknown top-level":        withFlows(okFlow) + "pingone_secrets = {}\n",
		"missing required":         withFlows(`{ f = { name = "x", graph = { nodes = {}, edges = {} } } }`),
		"null required":            withFlows(`{ f = { name = null, tier = 0, graph = { nodes = {}, edges = {} } } }`),
		"wrong type":               withFlows(`{ f = { name = 1, tier = 0, graph = { nodes = {}, edges = {} } } }`),
		"fractional int":           withFlows(`{ f = { name = "x", tier = 0.5, graph = { nodes = {}, edges = {} } } }`),
		"duplicate key":            withFlows(`{ f = { name = "x", tier = 0, graph = { nodes = {}, edges = {} } }, "f" = { name = "y", tier = 0, graph = { nodes = {}, edges = {} } } }`),
		"duplicate attribute":      withFlows(okFlow) + "parameters = {}\n",
		"block":                    withFlows(okFlow) + "terraform {}\n",
		"missing collection":       strings.Replace(withFlows(okFlow), "davinci_variables      = {}\n", "", 1),
		"too deep":                 withFlows(okFlow) + "x = " + strings.Repeat("[", 40) + strings.Repeat("]", 40) + "\n",
		"invalid utf8":             withFlows(`{ f = { name = "` + "\xff" + `", tier = 0, graph = { nodes = {}, edges = {} } } }`),
		"infinite number":          withFlows(`{ f = { name = "x", tier = 0, graph = { zoom = 1e999999, nodes = {}, edges = {} } } }`),
		"syntax error":             withFlows(`{ f = `),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src), "env.tfvars"); err == nil {
				t.Fatal("accepted invalid document")
			}
		})
	}
}

func TestParseAcceptsNegativeNumbersAndExponents(t *testing.T) {
	src := withFlows(`{ f = { name = "x", tier = 0, graph = { min_zoom = 1e-50, pan = { x = -1.5, y = -0 }, nodes = {}, edges = {} } } }`)
	doc, err := Parse([]byte(src), "env.tfvars")
	if err != nil {
		t.Fatal(err)
	}
	g := doc.Flows["f"].Graph
	if *g.MinZoom != 1e-50 || g.Pan.X != -1.5 {
		t.Fatalf("numbers decoded wrong: %v %v", *g.MinZoom, g.Pan.X)
	}
}

func TestParseRejectsOversizedInput(t *testing.T) {
	big := make([]byte, MaxFileSize+1)
	if _, err := Parse(big, "big.tfvars"); err == nil {
		t.Fatal("accepted oversized input")
	}
}

func TestParseErrorHasPosition(t *testing.T) {
	_, err := Parse([]byte(withFlows(`{ f = { name = var.secret_value, tier = 0, graph = { nodes = {}, edges = {} } } }`)), "env.tfvars")
	if err == nil || !strings.Contains(err.Error(), "env.tfvars:") {
		t.Fatalf("want a file:line:col position, got %v", err)
	}
}

func TestCanonicalCheckDetectsHandEdits(t *testing.T) {
	src, err := os.ReadFile(goldenPath("full"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(src), "\n  \"http\" = {", "\n  # hand comment\n  \"http\" = {", 1)
	doc, err := Parse([]byte(edited), "full.tfvars")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := Write(doc)
	if string(out) == edited {
		t.Fatal("canonical re-render kept a hand comment; the canonical check would miss edits")
	}
}
