package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"pingone-promote/internal/promote"
)

const rootCheckUsage = `Usage: pingone-promote check --root DIR [--customer-secrets FILE] [--terraform-fmt[=PATH]]
                             [--terraform-validate[=PATH]] [--plugin-dir DIR] [--envs a,b,c]
                             [--overlay-pattern '{env}.tfvars'] [--layout default|terraformer]
                             [--format text|json]

Validates a v2 config repository root (shared definitions plus one overlay
per environment) offline:

  - every overlay sets every required root variable, and no undeclared or
    secret key; import_ids keys are managed addresses;
  - import blocks are conditional on var.import_ids; no *.auto.tfvars;
  - every secret the root passes to the module is declared in
    customer_secrets.yml (with --customer-secrets);
  - no masked values, private keys or JWTs in any Terraform file;
  - terraform fmt (--terraform-fmt) and terraform init -backend=false +
    validate on a private copy (--terraform-validate; --plugin-dir for an
    offline provider mirror).

--envs lists environments that must have overlays (source and rollout);
overlays found on disk are always checked.

Exit status: 0 valid, 1 problems found, 2 usage or environment error.
`

func hasRootFlag(args []string) bool {
	for _, a := range args {
		if a == "--root" || a == "-root" || strings.HasPrefix(a, "--root=") || strings.HasPrefix(a, "-root=") {
			return true
		}
	}
	return false
}

func layoutFlag(name string) (promote.Layout, error) {
	switch name {
	case "", "default":
		return promote.DefaultLayout, nil
	case "terraformer":
		return promote.TerraformerLayout, nil
	}
	return promote.Layout{}, errors.New("--layout must be default or terraformer")
}

func runRootCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "")
	customer := fs.String("customer-secrets", "", "")
	envs := fs.String("envs", "", "")
	pattern := fs.String("overlay-pattern", "", "")
	layoutName := fs.String("layout", "", "")
	format := fs.String("format", "text", "")
	pluginDir := fs.String("plugin-dir", "", "")
	var tfFmt, tfValidate fmtFlag
	fs.Var(&tfFmt, "terraform-fmt", "")
	fs.Var(&tfValidate, "terraform-validate", "")
	usage := func(msg string) int {
		fmt.Fprintln(stderr, "pingone-promote check:", msg)
		fmt.Fprint(stderr, rootCheckUsage)
		return checkUsage
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, rootCheckUsage)
			return checkOK
		}
		return usage(err.Error())
	}
	if fs.NArg() != 0 || *root == "" {
		return usage("--root DIR is required and takes no file arguments")
	}
	if *format != "text" && *format != "json" {
		return usage("--format must be text or json")
	}
	l, err := layoutFlag(*layoutName)
	if err != nil {
		return usage(err.Error())
	}
	if *pattern != "" {
		l.OverlayPattern = *pattern
	}
	o := promote.CheckOptions{Root: *root, Layout: l, TerraformFmt: tfFmt.enabled, TerraformValidate: tfValidate.enabled, PluginDir: *pluginDir}
	o.TerraformBin = tfFmt.path
	if tfValidate.path != "" {
		o.TerraformBin = tfValidate.path
	}
	if *envs != "" {
		o.Envs = strings.Split(*envs, ",")
	}
	if *customer != "" {
		raw, err := readBounded(*customer)
		if err != nil {
			fmt.Fprintln(stderr, "pingone-promote check:", err)
			return checkUsage
		}
		o.CustomerSecrets = raw
	}
	rep, err := promote.CheckRoot(ctx, o)
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote check:", err)
		return checkUsage
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return checkUsage
		}
	} else {
		if rep.OK {
			fmt.Fprintf(stdout, "%s: OK (%d environments: %s)\n", rep.Root, len(rep.Envs), strings.Join(rep.Envs, ", "))
		} else {
			fmt.Fprintf(stdout, "%s: %d problem(s)\n", rep.Root, len(rep.Problems))
			for _, p := range rep.Problems {
				loc := p.Path
				if loc == "" {
					loc = "(root)"
				}
				fmt.Fprintf(stdout, "  %s: %s [%s]\n", loc, p.Message, p.Code)
			}
		}
		for _, p := range rep.Warnings {
			fmt.Fprintf(stdout, "  warning: %s: %s [%s]\n", p.Path, p.Message, p.Code)
		}
	}
	if !rep.OK {
		return checkProblems
	}
	return checkOK
}

// maxFileSize bounds a file read by the check command.
const maxFileSize = 32 << 20

// readBounded reads a regular file of at most maxFileSize bytes.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s", path)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("%s exceeds the maximum file size", path)
	}
	return data, nil
}
