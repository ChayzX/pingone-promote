package server

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/request"
	"pingone-promote/internal/secretscan"
)

type page struct {
	Session          *session
	Draft            *draft
	Sources, Rollout []string
	Requests         []RequestRecord
	Record           *RequestRecord
	RolloutRecords   []RolloutRecord
	Message          string
}

func canKeep(c request.ChangeOut) bool { return c.Status != "new" && c.File != "" }

var pages = template.Must(template.New("pages").Funcs(template.FuncMap{
	"canKeep": canKeep,
	"selected": func(d *draft, a string) bool {
		for _, v := range d.Request.Resources {
			if v.Address == a {
				return true
			}
		}
		return false
	},
	"excluded": func(d *draft, a string) bool {
		for _, v := range d.Request.Exclude {
			if v.Address == a {
				return true
			}
		}
		return false
	},
	"choice":  func(d *draft, a string) string { return d.Request.DependencyChoices[a] },
	"value":   func(d *draft, env, key string) string { return string(d.Request.EnvValues[env][key]) },
	"mapping": func(d *draft, key string) string { return d.Request.SecretMappings[key] },
	"visible": func(d *draft, r exporter.Resource) bool {
		if d.TypeFilter != "" && d.TypeFilter != r.Type {
			return false
		}
		q := strings.ToLower(d.Query)
		return q == "" || strings.Contains(strings.ToLower(r.Name+" "+r.ID+" "+r.Address.String()), q)
	},
	"types": func(d *draft) []string {
		seen := map[string]bool{}
		for _, r := range d.Catalog {
			seen[r.Type] = true
		}
		var out []string
		for k := range seen {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	},
	"revision":    func(d *draft) string { return strconv.FormatUint(d.Revision, 10) },
	"busy":        busy,
	"recordLimit": func() int { return repositoryRecordLimit },
}).Parse(htmlPages))

func (s *Server) render(w http.ResponseWriter, name string, p page) {
	var b bytes.Buffer
	if err := pages.ExecuteTemplate(&b, name, p); err != nil {
		http.Error(w, "Could not render page", 500)
		return
	}
	content := s.redact(b.Bytes())
	// Catalog names and durable GitLab metadata are untrusted too. Escaping
	// protects HTML syntax, while this last boundary protects sensitive text
	// that did not pass through the request engine's artifact scan.
	if len(secretscan.ScanText("page", content)) > 0 {
		http.Error(w, "Page content withheld because it may contain sensitive values; ask an operator to review the source metadata privately.", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

const htmlPages = `{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>PingOne Promote</title><link rel="stylesheet" href="/assets/style.css"><script src="/assets/app.js" defer></script></head><body><header><a href="/">PingOne Promote</a><nav><a href="/requests">Requests and rollout</a></nav></header><main>{{end}}
{{define "foot"}}</main><footer>Source-only preparation. The enterprise pipeline plans and applies. Drafts expire and are lost on restart.</footer></body></html>{{end}}
{{define "home"}}{{template "head" .}}<h1>New promotion request</h1><p>Select a source. Your configured rollout is {{range $i,$v:=.Rollout}}{{if $i}} → {{end}}{{$v}}{{end}}.</p><form action="/drafts" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><label>Your name <input name="name" value="{{.Session.Name}}" maxlength="100" required></label><p>Typed names are recorded as <strong>unverified</strong> and do not grant authorization.</p><label>Source <select name="source">{{range .Sources}}<option>{{.}}</option>{{end}}</select></label><label>Scope <select name="scope"><option value="selected">Selected resources</option><option value="all">Whole supported environment</option></select></label><label><input type="checkbox" name="confirmAll" value="yes"> I confirm a whole-environment export when that scope is selected</label><label><input type="checkbox" name="upstream" value="yes" checked> Include upstream dependencies</label><label>Delivery <select name="mode"><option value="mr">Merge request</option><option value="branch">Branch only</option></select></label><button>Create draft</button></form>{{template "foot" .}}{{end}}
{{define "draft"}}{{template "head" .}}{{$d:=.Draft}}<article data-draft="{{$d.ID}}" data-state="{{$d.State}}"><h1>Request from {{$d.Request.Source}}</h1><p>Requested by <strong>{{$d.Request.RequestedBy}}</strong> · {{$d.Request.Scope}} · {{$d.Request.Mode}} · revision {{$d.Revision}}</p><p role="status"><strong>{{$d.State}}</strong>: {{$d.Message}}</p>{{if busy $d}}<p>Work continues independently if you leave this page.</p>{{else if eq $d.State "submitted"}}<p>Commit <code>{{$d.Report.CommitID}}</code>. {{if $d.Report.MRUrl}}<a href="{{$d.Report.MRUrl}}" rel="noreferrer">Open merge request</a>{{end}} <a href="/requests/{{$d.ID}}">Request and rollout</a></p>{{else if eq $d.State "partial"}}<p><a href="/requests">Inspect GitLab request history</a> before retrying the exact reviewed publication.</p><form action="/drafts/{{$d.ID}}/submit" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><input type="hidden" name="revision" value="{{$d.Revision}}"><button>Reconcile reviewed publication</button></form>{{else if eq $d.State "no_changes"}}<p>No publication was necessary.</p>{{else}}
<section><h2>Search the source</h2><form action="/drafts/{{$d.ID}}/search" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><input type="hidden" name="revision" value="{{$d.Revision}}"><label>Name, ID, or address <input name="query" value="{{$d.Query}}" maxlength="200"></label><label>Resource type <select name="type"><option value="">All supported types</option>{{range types $d}}<option {{if eq . $d.TypeFilter}}selected{{end}}>{{.}}</option>{{end}}</select></label><button>Search catalog</button></form></section>
<form action="/drafts/{{$d.ID}}/selection" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><input type="hidden" name="revision" value="{{$d.Revision}}"><section><h2>Selection</h2><p>Includes and excludes use exact catalog addresses. Excluded dependencies may need an existing Git object or an ID for each environment.</p>{{if $d.Catalog}}<table><thead><tr><th>Include</th><th>Exclude</th><th>Resource</th><th>ID</th></tr></thead><tbody>{{range $d.Catalog}}{{$a:=.Address.String}}{{if visible $d .}}<tr><td><input aria-label="Include {{$a}}" type="checkbox" name="resource" value="{{$a}}" {{if selected $d $a}}checked{{end}}></td><td><input aria-label="Exclude {{$a}}" type="checkbox" name="exclude" value="{{$a}}" {{if excluded $d $a}}checked{{end}}></td><td>{{.Name}}<br><code>{{$a}}</code></td><td>{{.ID}}</td></tr>{{else}}{{if selected $d $a}}<input type="hidden" name="resource" value="{{$a}}">{{end}}{{if excluded $d $a}}<input type="hidden" name="exclude" value="{{$a}}">{{end}}{{end}}{{end}}</tbody></table>{{else}}<p>Search to load the catalog, or preview a confirmed whole-environment request.</p>{{end}}</section>
{{if $d.Report}}<section><h2>Dependencies and changes</h2><table><thead><tr><th>Resource</th><th>Role</th><th>Action / comparison</th><th>Choice</th></tr></thead><tbody>{{range $d.Report.Changes}}<tr><td><code>{{.Address}}</code>{{if .Reason}}<p>{{.Reason}}</p>{{end}}</td><td>{{.Role}}</td><td>{{.Action}} / {{.Status}}</td><td>{{if or (eq .Role "dependency") (eq .Role "environment")}}{{$current:=choice $d .Address}}<select name="choice.{{.Address}}"><option value="" {{if eq $current ""}}selected{{end}}>Default</option><option value="update" {{if eq $current "update"}}selected{{end}}>Update from source</option>{{if canKeep .}}<option value="keep" {{if eq $current "keep"}}selected{{end}}>Keep Git version</option>{{end}}<option value="exclude" {{if eq $current "exclude"}}selected{{end}}>Exclude</option></select>{{else}}Selected / fixed{{end}}</td></tr>{{end}}</tbody></table></section>
<section><h2>Dependency graph</h2><p>Each source resource lists the resources it references. Flow companions and global variables are included by the export engine.</p><table><thead><tr><th>Resource</th><th>References</th></tr></thead><tbody>{{range $d.Report.Graph}}<tr><td><code>{{.Address}}</code></td><td>{{range .Refs}}<code>{{.}}</code><br>{{else}}None{{end}}</td></tr>{{end}}</tbody></table></section><section><h2>Environment readiness</h2><p>Enter JSON values, such as <code>"https://example.test"</code>, <code>42</code>, or <code>["one"]</code>. Never enter a secret value.</p>{{range $env,$bs:=$d.Report.Blockers}}<h3>{{$env}}</h3>{{range $bs}}<p>{{.Code}}: {{.Message}}</p>{{if and .Var (eq .Code "missing_value") (ne $env "_request")}}<label>{{.Var}} ({{$env}}, {{index $d.Report.ValueTypes .Var}}) <input name="value.{{$env}}.{{.Var}}" value="{{value $d $env .Var}}" maxlength="4096"></label>{{end}}{{end}}{{else}}<p>No readiness blockers.</p>{{end}}{{range $env,$values:=$d.Request.EnvValues}}{{range $key,$value:=$values}}<label>{{$key}} ({{$env}}, {{index $d.Report.ValueTypes $key}}) <input name="value.{{$env}}.{{$key}}" value="{{value $d $env $key}}" maxlength="4096"></label>{{end}}{{end}}</section>
<section><h2>Vault secret names</h2><p>Names only. Create the corresponding values in Vault for every environment before deployment.</p>{{range $d.Report.RequiredSecrets}}<label>{{.Key}} → Vault name <input name="secret.{{.Key}}" value="{{mapping $d .Key}}" maxlength="128" pattern="[A-Za-z][A-Za-z0-9_]*" placeholder="{{.VarName}}"></label>{{end}}</section>{{end}}<button>Save selection, values, and choices</button></form>
<form action="/drafts/{{$d.ID}}/preview" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><input type="hidden" name="revision" value="{{$d.Revision}}"><button>Preview current revision</button></form>
{{if eq $d.State "ready"}}<form action="/drafts/{{$d.ID}}/submit" method="post"><input type="hidden" name="csrf" value="{{.Session.CSRF}}"><input type="hidden" name="revision" value="{{$d.Revision}}"><button>Publish reviewed {{if eq $d.Request.Mode "mr"}}merge request{{else}}branch{{end}}</button></form>{{end}}
{{end}}{{if $d.Report}}<section><h2>Review</h2><p class="warning">Additive file merging cannot prove a future plan is non-destructive. Provider changes may replace or destroy resources. Inspect enterprise plans and consider lifecycle prevent_destroy guards. Recover by rolling forward with a reviewed correcting commit; a Git revert may remove resources and cause destruction.</p>{{if $d.Report.Error}}<p role="alert">{{$d.Report.Error.Message}}</p>{{end}}{{range $d.Report.Warnings}}<p>{{.}}</p>{{end}}{{if $d.Report.Diff}}<pre>{{$d.Report.Diff}}</pre>{{end}}{{if $d.Report.DiffTruncated}}<p>Diff truncated. Review all published files in GitLab.</p>{{end}}</section>{{end}}</article>{{template "foot" .}}{{end}}
{{define "requests"}}{{template "head" .}}<h1>Requests and rollout</h1><p>This view reads up to {{recordLimit}} merge requests (most recently updated first) and {{recordLimit}} matching branches. Older requests may be omitted; open GitLab for complete history. Rollout checks read up to {{recordLimit}} jobs. Missing deployment evidence remains unknown.</p><p>{{.Message}}</p>{{if .Record}}<h2>{{.Record.Title}}</h2><p>{{.Record.State}} · <code>{{.Record.Branch}}</code> · <code>{{.Record.Commit}}</code></p>{{if .Record.URL}}<a href="{{.Record.URL}}" rel="noreferrer">Open in GitLab</a>{{end}}<table><thead><tr><th>Environment</th><th>Apply job</th><th>Status</th></tr></thead><tbody>{{range .RolloutRecords}}<tr><td>{{.Environment}}</td><td>{{.Job}}</td><td>{{if .URL}}<a href="{{.URL}}" rel="noreferrer">{{.Status}}</a>{{else}}{{.Status}}{{end}}</td></tr>{{end}}</tbody></table><p>Only pipelines linked to this request's merge or squash commit count as rollout evidence. Missing evidence is unknown.</p>{{else}}<table><thead><tr><th>Request</th><th>State</th><th>Commit</th></tr></thead><tbody>{{range .Requests}}<tr><td><a href="/requests/{{.ID}}">{{.Title}}</a><br><code>{{.Branch}}</code></td><td>{{.State}}</td><td><code>{{.Commit}}</code></td></tr>{{else}}<tr><td colspan="3">No published requests found.</td></tr>{{end}}</tbody></table>{{end}}{{template "foot" .}}{{end}}`

const appJS = `const draft=document.querySelector('[data-draft]');if(draft&&['queued','searching','previewing','submitting'].includes(draft.dataset.state)){let stopped=false;const poll=async()=>{try{const r=await fetch('/drafts/'+encodeURIComponent(draft.dataset.draft)+'/status',{credentials:'same-origin',cache:'no-store'});if(!r.ok){stopped=true;return;}const data=await r.json();if(data.State!==draft.dataset.state){location.reload();return;}}catch{}if(!stopped)setTimeout(poll,1500);};setTimeout(poll,1000);}`
const styleCSS = `:root{font:16px/1.5 system-ui,sans-serif;color:#18283b;background:#f5f7fa}body{margin:0}header,main,footer{max-width:1100px;margin:auto;padding:1.5rem}header{display:flex;justify-content:space-between;border-bottom:1px solid #ccd5df}main{background:white}footer{font-size:.9rem;color:#47576a}a{color:#0058a6}form,section{margin:1.5rem 0}label{display:block;margin:.8rem 0}input:not([type=checkbox]),select{font:inherit;max-width:100%;padding:.4rem;border:1px solid #8494a6;border-radius:4px}input:not([type=checkbox]){width:28rem}button{font:inherit;padding:.6rem 1rem;background:#0058a6;color:white;border:0;border-radius:4px;cursor:pointer}table{width:100%;border-collapse:collapse}td,th{text-align:left;vertical-align:top;padding:.6rem;border-bottom:1px solid #d8e0e9}code,pre{font:13px/1.5 ui-monospace,monospace;overflow-wrap:anywhere}pre{padding:1rem;background:#f0f3f7;overflow:auto;white-space:pre-wrap}.warning{padding:1rem;background:#fff3cf;border-left:4px solid #b57b00}input:focus,select:focus,button:focus,a:focus{outline:3px solid #b57b00;outline-offset:2px}@media(max-width:700px){main{padding:1rem}table{display:block;overflow:auto}header{display:block}}`
