package server

import (
	"regexp"
	"sort"
	"strings"

	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/request"
)

// This file turns engine output (addresses, variable names, blocker codes,
// warnings) into the plain language the pages show. Technical names stay
// available in small print for people who look at Git.

// itemName is the source environment's name for an address, from the
// draft's search list, or the name decoded from the Terraform label.
func itemName(d *Draft, addr string) string {
	if c := d.catalogs[d.Source]; c != nil {
		for _, it := range c.items {
			if it.Address == addr && it.Name != "" {
				return it.Name
			}
		}
	}
	for _, list := range [][]Item{d.Selected, d.Excluded, d.Results} {
		for _, it := range list {
			if it.Address == addr && it.Name != "" {
				return it.Name
			}
		}
	}
	_, label, _ := strings.Cut(addr, ".")
	if n, ok := neutralize.DecodeLabel(label); ok && n != "" {
		return n
	}
	return strings.TrimPrefix(label, neutralize.LabelPrefix)
}

// describe names an item in a sentence: the kind and the quoted name.
func describe(d *Draft, addr string) string {
	typ, _, _ := strings.Cut(addr, ".")
	return resourceLabel(typ) + " “" + itemName(d, addr) + "”"
}

// attributeWords are plain names for attributes that commonly differ per
// environment; other attribute paths are spelled out from their parts.
var attributeWords = map[string]string{
	"oidc_options.redirect_uris":             "redirect URLs",
	"oidc_options.post_logout_redirect_uris": "sign-out redirect URLs",
	"oidc_options.initiate_login_uri":        "login start URL",
	"oidc_options.target_link_uri":           "target link URL",
	"oidc_options.home_page_url":             "home page URL",
	"oidc_options.jwks_url":                  "JWKS URL",
	"oauth.redirect_uris":                    "redirect URLs",
	"oauth.logout_uris":                      "logout URLs",
	"saml_options.acs_urls":                  "ACS URLs",
	"saml_options.sp_entity_id":              "SP entity ID",
	"login_page_url":                         "login page URL",
	"email.from.address":                     "sender email address",
	"email.reply_to.address":                 "reply-to email address",
	"from.email_address":                     "sender email address",
	"reply_to.email_address":                 "reply-to email address",
	"domain_name":                            "domain name",
	"audience":                               "audience",
	"base_urls":                              "base URLs",
	"servers":                                "LDAP servers",
	"bind_dn":                                "bind DN",
	"relying_party_id":                       "relying party ID",
	"http_endpoint_url":                      "endpoint URL",
	"language_id":                            "language ID",
	"site_key":                               "site key",
	"addresses":                              "alert email addresses",
}

func attributeLabel(path string) string {
	if w, ok := attributeWords[path]; ok {
		return w
	}
	parts := strings.Split(path, ".")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, "_", " ")
	}
	return strings.Join(parts, " › ")
}

// valueLabel explains a per-environment value: what it is and an example
// from the source environment.
func valueLabel(d *Draft, res *request.Result, name string) (label, hint string) {
	v, ok := res.Variables[name]
	switch {
	case name == neutralize.EnvironmentIDVariable:
		return "The PingOne environment ID of this environment", "Terraform applies this environment's changes to the PingOne environment with this ID (pingone_environment_id). It is missing from this environment's .tfvars file. The operator can list every environment's ID under environmentIds in the server configuration; it is then filled in automatically."
	case !ok:
		// A root variable of the repository that this copy does not set.
		return "Repository setting " + name, "Required by the repository, which has no value for it in this environment. If your pipeline supplies it (a GitLab CI variable), the operator adds it to repository.ciVariables in the server configuration instead."
	case v.Kind == "excluded_ref" && v.Resource != "":
		label = "ID of the " + describe(d, v.Resource) + " in this environment"
		hint = "It is not part of this copy, so each environment refers to the one it already has."
	case strings.HasPrefix(name, "davinci_form_"):
		// terraformer's own wording names no form.
		label = "ID of the DaVinci form “" + strings.TrimPrefix(name, "davinci_form_") + "” in this environment"
		hint = "DaVinci forms are not copied, so each environment refers to its own."
	case v.Attribute != "" && v.Resource != "":
		label = attributeLabel(v.Attribute) + " of the " + describe(d, v.Resource)
	case v.Description != "":
		label = v.Description
	default:
		label = name
	}
	if v.SourceValue != "" {
		ex := "In " + d.Source + ": " + v.SourceValue
		if hint != "" {
			hint += " " + ex
		} else {
			hint = ex
		}
	}
	return label, hint
}

// blockerView is one reason an environment is not ready.
type blockerView struct {
	Text string
	// Tech is the technical detail (file and variable) for Git readers.
	Tech string
}

func blockerText(d *Draft, res *request.Result, env string, b request.BlockerOut) blockerView {
	switch {
	case b.Code == "missing_value" && b.Var != "":
		label, _ := valueLabel(d, res, b.Var)
		return blockerView{Text: "Needs a value: " + label, Tech: env + ".tfvars · " + b.Var}
	case b.Code == "missing_dependency" && b.Address != "":
		return blockerView{Text: "Needs the " + describe(d, b.Address) + ": the request uses it, but it is neither in the request nor in Git. Add it (or copy with “what it depends on”), or leave out what uses it.",
			Tech: b.Address}
	case b.Code == "missing_import" && b.Address != "":
		return blockerView{Text: "Needs the ID of the existing " + describe(d, b.Address) + ". It already exists in every environment, so it is adopted, not created.",
			Tech: env + ".tfvars import_ids · " + b.Address}
	}
	return blockerView{Text: b.Message}
}

var notReadRE = regexp.MustCompile(`^(pingone_[a-z0-9_]+) was not read: (.*)$`)

// splitWarnings separates "this type could not be read" notes (expected
// when a service is not licensed) from the warnings worth reading.
func splitWarnings(ws []string) (skipped, other []string) {
	for _, w := range ws {
		if m := notReadRE.FindStringSubmatch(w); m != nil {
			// Only "cannot read" and "does not exist" are expected; any
			// other refusal stays a warning worth reading.
			switch why := m[2]; {
			case strings.Contains(why, "(HTTP 403"):
				skipped = append(skipped, resourceLabel(m[1])+": the worker may not read it, or the service is not licensed")
				continue
			case strings.Contains(why, "(HTTP 404"):
				skipped = append(skipped, resourceLabel(m[1])+": not available in this environment")
				continue
			}
		}
		other = append(other, w)
	}
	sort.Strings(skipped)
	return skipped, other
}

// onlyMissingValues reports whether every blocker is something the person
// fills in on this page.
func onlyMissingValues(res *request.Result) bool {
	if len(res.Blockers) == 0 {
		return false
	}
	for _, bs := range res.Blockers {
		for _, b := range bs {
			if !(b.Code == "missing_value" && b.Var != "") && !(b.Code == "missing_import" && b.Address != "") {
				return false
			}
		}
	}
	return true
}
