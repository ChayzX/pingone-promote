package checks

import (
	"context"
	"fmt"
	"strings"

	"pingone-promote/internal/pingone"
)

// PingOnePreflight runs read-only capability probes for each environment:
// identity, the Configuration Management opt-in and the promotions API. It
// distinguishes a feature that is not active from a worker that lacks a role,
// because PingOne answers both with HTTP 403.
func PingOnePreflight(ctx context.Context, c *pingone.Client, envs map[string]string) Report {
	r := Report{Title: "PingOne preflight"}
	labels := make([]string, 0, len(envs))
	for label := range envs {
		labels = append(labels, label)
	}
	// Stable order: source before target, then anything else.
	order := map[string]int{"source": 0, "target": 1}
	for i := range labels {
		for j := i + 1; j < len(labels); j++ {
			oi, iok := order[labels[i]]
			oj, jok := order[labels[j]]
			if (!iok && jok) || (iok && jok && oj < oi) || (!iok && !jok && labels[j] < labels[i]) {
				labels[i], labels[j] = labels[j], labels[i]
			}
		}
	}
	for _, label := range labels {
		env := envs[label]
		name := func(s string) string { return label + " " + s }

		var meta struct {
			ID           string      `json:"id"`
			Name         string      `json:"name"`
			Type         string      `json:"type"`
			Organization pingone.Ref `json:"organization"`
		}
		if err := c.Do(ctx, "GET", pingone.EnvPath(env), "", nil, &meta); err != nil {
			r.Fail(name("environment"), "cannot read environment %s: %s", env, explain(err))
			continue
		}
		if meta.ID != env {
			r.Fail(name("environment"), "environment response identity %q differs from %s", meta.ID, env)
			continue
		}
		r.Pass(name("environment"), "%s (%s, type %s, organization %s)", meta.Name, env, meta.Type, meta.Organization.ID)
		if strings.EqualFold(meta.Type, "PRODUCTION") {
			r.Warn(name("environment type"), "environment type is PRODUCTION; early-access Configuration Management may be limited to Sandbox environments")
		}

		var features struct {
			Embedded struct {
				Features []struct {
					Name  string `json:"name"`
					Value bool   `json:"value"`
				} `json:"features"`
			} `json:"_embedded"`
			Features []struct {
				Name  string `json:"name"`
				Value bool   `json:"value"`
			} `json:"features"`
		}
		if err := c.Do(ctx, "GET", pingone.EnvPath(env)+"/earlyAccessFeatures", "", nil, &features); err != nil {
			r.Warn(name("opt-in"), "cannot read early access features (%s); confirm the Configuration Management opt-in in the console", explain(err))
		} else {
			list := append(features.Features, features.Embedded.Features...)
			found, on := false, false
			for _, f := range list {
				if strings.EqualFold(f.Name, "Configuration Management") {
					found, on = true, f.Value
				}
			}
			switch {
			case on:
				r.Pass(name("opt-in"), "Configuration Management opt-in is on")
			case found:
				r.Fail(name("opt-in"), "Configuration Management opt-in is off; enable it under Environment Properties > Manage Opt-Ins")
			default:
				r.Warn(name("opt-in"), "Configuration Management is not listed as an early access feature (it may be generally available)")
			}
		}

		var promotions any
		err := c.Do(ctx, "GET", pingone.EnvPath(env)+"/promotions", "", nil, &promotions)
		switch {
		case err == nil:
			r.Pass(name("promotions API"), "promotions API answers for this worker")
		case strings.Contains(strings.ToLower(err.Error()), "not enabled"):
			r.Fail(name("promotions API"), "Configuration Management is not active for this environment even if opted in; check the license/entitlement with Ping (%s)", explain(err))
		case strings.Contains(err.Error(), "HTTP 403"):
			r.Fail(name("promotions API"), "worker is denied; grant Environment Admin and Promotion Admin on this environment (%s)", explain(err))
		default:
			r.Fail(name("promotions API"), "promotions API failed: %s", explain(err))
		}
	}
	return r
}

func explain(err error) string {
	s := oneLine(err.Error())
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

// EnvironmentLabels builds the probe map, skipping empty IDs and duplicates.
func EnvironmentLabels(source, target string) map[string]string {
	m := map[string]string{}
	if target != "" {
		m["target"] = target
	}
	if source != "" && source != target {
		m["source"] = source
	}
	return m
}

// Describe is a short identity string for logs.
func Describe(envs map[string]string) string {
	parts := []string{}
	for k, v := range envs {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, " ")
}
