package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/tfvars"
)

// Exit codes for `pingone-promote check`.
const (
	checkOK       = 0 // the file is valid and canonical
	checkProblems = 1 // the file has problems (including parse errors)
	checkUsage    = 2 // bad arguments, unreadable file, or formatter unavailable
)

const checkUsageText = `Usage: pingone-promote check <environments/ENV.tfvars> [--format text|json] [--required-secrets] [--terraform-fmt[=PATH]] [--customer-secrets PATH]

Validates a tfvars file offline. It never contacts PingOne, GitLab or any
other network service.

  --format text|json     output format (default text)
  --required-secrets     list the pingone_secrets key names the file needs
  --terraform-fmt[=PATH] also pipe the file through "terraform fmt -" (or
                         "tofu fmt -") and require identical output; PATH
                         names the binary, otherwise terraform then tofu are
                         looked up on PATH
  --customer-secrets PATH
                         require every @@secret: key to be declared as
                         TF_VAR_<key> in the pipeline's customer_secrets.yml;
                         commented-out entries do not count. Unused entries
                         and entries outside the standard Vault path are
                         warnings

Exit status: 0 valid, 1 problems found, 2 usage or environment error.
`

// fmtFlag is a boolean-or-path flag: --terraform-fmt or --terraform-fmt=PATH.
type fmtFlag struct {
	enabled bool
	path    string
}

func (f *fmtFlag) String() string   { return f.path }
func (f *fmtFlag) IsBoolFlag() bool { return true }
func (f *fmtFlag) Set(v string) error {
	switch v {
	case "true":
		f.enabled, f.path = true, ""
	case "false":
		f.enabled, f.path = false, ""
	case "":
		return errors.New("empty formatter path")
	default:
		f.enabled, f.path = true, v
	}
	return nil
}

type checkOutput struct {
	File               string                  `json:"file"`
	OK                 bool                    `json:"ok"`
	Canonical          bool                    `json:"canonical"`
	TerraformFmt       string                  `json:"terraform_fmt,omitempty"`
	Problems           []tfvars.Problem        `json:"problems"`
	RequiredSecrets    []string                `json:"required_secrets"`
	RequiredParameters []string                `json:"required_parameters"`
	Warnings           []tfvars.Problem        `json:"warnings"`
	CustomerSecrets    *pipelinesecrets.Report `json:"customer_secrets,omitempty"`
}

// Problem codes added by the check command on top of tfvars.Validate.
const (
	codeParse        = "parse"
	codeNotCanonical = "not_canonical"
	codeTerraformFmt = "terraform_fmt"

	codeSecretUndeclared = "secret_undeclared"
	codeSecretCommented  = "secret_commented_out"
	codeSecretUnused     = "secret_unused"
	codeSecretPath       = "secret_path_unusual"
)

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "")
	requiredSecrets := fs.Bool("required-secrets", false, "")
	customerSecrets := fs.String("customer-secrets", "", "")
	var tfFmt fmtFlag
	fs.Var(&tfFmt, "terraform-fmt", "")
	usage := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(stderr, "pingone-promote check:", msg)
		}
		fmt.Fprint(stderr, checkUsageText)
		return checkUsage
	}
	// Accept flags before and after the file argument.
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, checkUsageText)
				return checkOK
			}
			return usage(err.Error())
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(files) != 1 {
		return usage("exactly one tfvars file is required")
	}
	if *format != "text" && *format != "json" {
		return usage("--format must be text or json")
	}
	path := files[0]
	src, err := readBounded(path)
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote check:", err)
		return checkUsage
	}

	var secretsFile *pipelinesecrets.File
	if *customerSecrets != "" {
		raw, err := readBounded(*customerSecrets)
		if err == nil {
			secretsFile, err = pipelinesecrets.Parse(raw)
		}
		if err != nil {
			fmt.Fprintln(stderr, "pingone-promote check:", err)
			return checkUsage
		}
	}

	out := checkOutput{File: path, Problems: []tfvars.Problem{}, Warnings: []tfvars.Problem{}, RequiredSecrets: []string{}, RequiredParameters: []string{}}
	doc, err := tfvars.Parse(src, path)
	if err != nil {
		out.Problems = append(out.Problems, tfvars.Problem{Code: codeParse, Message: err.Error()})
	} else {
		report := tfvars.Validate(doc)
		out.Problems = append(out.Problems, report.Problems...)
		out.RequiredSecrets = append(out.RequiredSecrets, report.RequiredSecrets...)
		out.RequiredParameters = append(out.RequiredParameters, report.RequiredParameters...)
		canonical, err := tfvars.Write(doc)
		switch {
		case err != nil:
			// Validate has already reported the field the writer rejects.
		case string(canonical) == string(src):
			out.Canonical = true
		default:
			out.Problems = append(out.Problems, tfvars.Problem{Code: codeNotCanonical,
				Message: "file differs from the canonical pingone-promote layout at line " + strconv.Itoa(firstDifferentLine(canonical, src)) + "; regenerate it instead of editing by hand"})
		}
	}
	if secretsFile != nil && doc != nil {
		r := secretsFile.Check(pipelinesecrets.DefaultConfig, out.RequiredSecrets)
		out.CustomerSecrets = &r
		where := *customerSecrets
		for _, k := range r.Undeclared {
			out.Problems = append(out.Problems, tfvars.Problem{Code: codeSecretUndeclared, Path: where,
				Message: "secret " + k + " is not declared; add TF_VAR_" + k + " or map the key to an existing entry"})
		}
		for _, k := range r.CommentedOut {
			out.Problems = append(out.Problems, tfvars.Problem{Code: codeSecretCommented, Path: where,
				Message: "secret " + k + " is declared only in a comment; uncomment TF_VAR_" + k})
		}
		for _, k := range r.Unused {
			out.Warnings = append(out.Warnings, tfvars.Problem{Code: codeSecretUnused, Path: where,
				Message: "TF_VAR_" + k + " is declared but this file does not use it"})
		}
		for _, k := range r.PatternMismatch {
			out.Warnings = append(out.Warnings, tfvars.Problem{Code: codeSecretPath, Path: where,
				Message: "TF_VAR_" + k + " is not at " + pipelinesecrets.DefaultConfig.PathPrefix + "/<field>@" + pipelinesecrets.DefaultConfig.Mount})
		}
	}
	if tfFmt.enabled {
		_, err := tfvars.FormatWithTerraform(ctx, tfFmt.path, src)
		var missing *tfvars.FormatterNotFoundError
		var mismatch *tfvars.FormatMismatchError
		switch {
		case err == nil:
			out.TerraformFmt = "ok"
		case errors.As(err, &missing):
			fmt.Fprintln(stderr, "pingone-promote check:", err)
			return checkUsage
		case errors.As(err, &mismatch):
			out.TerraformFmt = "differs"
			out.Problems = append(out.Problems, tfvars.Problem{Code: codeTerraformFmt, Message: err.Error()})
		default:
			out.TerraformFmt = "failed"
			out.Problems = append(out.Problems, tfvars.Problem{Code: codeTerraformFmt, Message: err.Error()})
		}
	}
	out.OK = len(out.Problems) == 0

	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return checkUsage
		}
	} else {
		writeCheckText(stdout, out, *requiredSecrets)
	}
	if !out.OK {
		return checkProblems
	}
	return checkOK
}

func writeCheckText(w io.Writer, out checkOutput, listSecrets bool) {
	if out.OK {
		fmt.Fprintf(w, "%s: OK (tfvars schema v%d, canonical)\n", out.File, tfvars.SchemaVersion)
	} else {
		fmt.Fprintf(w, "%s: %d problem(s)\n", out.File, len(out.Problems))
		for _, p := range out.Problems {
			loc := p.Path
			if loc == "" {
				loc = "(file)"
			}
			fmt.Fprintf(w, "  %s: %s [%s]\n", loc, p.Message, p.Code)
		}
	}
	for _, p := range out.Warnings {
		fmt.Fprintf(w, "  warning: %s: %s [%s]\n", p.Path, p.Message, p.Code)
	}
	if listSecrets {
		fmt.Fprintf(w, "required pingone_secrets keys (%d):\n", len(out.RequiredSecrets))
		for _, k := range out.RequiredSecrets {
			fmt.Fprintf(w, "  %s\n", k)
		}
		if out.CustomerSecrets != nil && len(out.CustomerSecrets.Candidates) > 0 {
			fmt.Fprintf(w, "declared pipeline secrets (candidates for mapping):\n")
			for _, k := range out.CustomerSecrets.Candidates {
				fmt.Fprintf(w, "  %s\n", k)
			}
		}
	}
}

// readBounded reads a regular file of at most tfvars.MaxFileSize bytes.
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
	data, err := io.ReadAll(io.LimitReader(f, tfvars.MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	if len(data) > tfvars.MaxFileSize {
		return nil, fmt.Errorf("%s exceeds the maximum tfvars size", path)
	}
	return data, nil
}

func firstDifferentLine(a, b []byte) int {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	if len(al) < len(bl) {
		return len(al) + 1
	}
	return len(bl) + 1
}
