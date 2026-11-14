package nativepoc

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

func heading(r Review) string {
	if r.Rollback {
		return fmt.Sprintf("Rollback plan %s: %s back to before the promotion", r.Ticket, r.Target.Name)
	}
	return fmt.Sprintf("Promotion plan %s: %s → %s", r.Ticket, r.Source.Name, r.Target.Name)
}

func summary(r Review) string {
	c, u, d := r.Counts()
	parts := []string{}
	for _, x := range []struct {
		n    int
		word string
	}{{c, "created"}, {u, "updated"}, {d, "deleted"}} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", x.n, x.word))
		}
	}
	if len(parts) == 0 {
		return "Nothing changes in " + r.Target.Name + "."
	}
	return strings.Join(parts, ", ") + " in " + r.Target.Name + "."
}

func mdCell(s string) string {
	s = strings.NewReplacer("|", "\\|", "\n", " ", "`", "'").Replace(s)
	if s == "" {
		return ""
	}
	return "`" + s + "`"
}

// Markdown is the note posted on the merge request. Long items are cut
// short; the HTML report has everything.
func Markdown(r Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", heading(r), summary(r))
	if r.Description != "" {
		fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(r.Description, "\n", " "))
	}
	if len(r.Warnings) > 0 {
		b.WriteString("**Look at these first**\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- ⚠️ %s\n", w)
		}
		b.WriteString("\n")
	}
	for _, it := range r.Items {
		tag := ""
		if !it.Selected {
			tag = " _(dependency, added by PingOne)_"
		}
		fmt.Fprintf(&b, "### %s %s “%s”%s\n\n", strings.ToUpper(it.Action), it.Type, it.Name, tag)
		switch it.Action {
		case "create":
			fmt.Fprintf(&b, "New in %s (%d fields).\n\n", r.Target.Name, it.Fields)
		case "delete":
			fmt.Fprintf(&b, "Removed from %s (%d fields).\n\n", r.Target.Name, it.Fields)
		default:
			if len(it.Changes) == 0 {
				b.WriteString("No field differs.\n\n")
				continue
			}
			b.WriteString("| Field | Now in " + r.Target.Name + " | After |\n| --- | --- | --- |\n")
			for i, c := range it.Changes {
				if i == 25 {
					fmt.Fprintf(&b, "| … %d more in the report | | |\n", len(it.Changes)-25)
					break
				}
				before, after := mdCell(c.Before), mdCell(c.After)
				switch c.Kind {
				case "add":
					before = "_not set_"
				case "remove":
					after = "_removed_"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", strings.ReplaceAll(c.Path, "|", "\\|"), before, after)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "Promotion `%s` · plan digest `%s`\n\nThe apply job runs this promotion only while its operations still have this digest.\n", r.PromotionID, r.Digest[:16])
	return b.String()
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"upper": strings.ToUpper, "heading": heading, "summary": summary,
	"counts": func(r Review) struct{ C, U, D int } {
		c, u, d := r.Counts()
		return struct{ C, U, D int }{c, u, d}
	},
	"short": func(s string) string {
		if len(s) > 16 {
			return s[:16]
		}
		return s
	},
}).Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{heading .}}</title>
<style>
:root{--bg:#fff;--fg:#1b1f24;--mut:#5b6470;--line:#d9dee4;--card:#f6f8fa;--add:#0a6b2d;--addbg:#e6f6ea;--del:#a1131f;--delbg:#fdecee;--upd:#7a4d00;--updbg:#fff4d6;--warn:#7a4d00;--warnbg:#fff8e1}
@media (prefers-color-scheme:dark){:root{--bg:#0f1317;--fg:#e6eaee;--mut:#9aa4af;--line:#2a323b;--card:#161b21;--add:#7ee2a0;--addbg:#12301d;--del:#ff9aa4;--delbg:#3a161b;--upd:#ffd27a;--updbg:#3a2c0c;--warn:#ffd27a;--warnbg:#2f2610}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:960px;margin:0 auto;padding:20px 16px 48px}
h1{font-size:1.45rem;line-height:1.25;margin:0 0 4px;text-wrap:balance}h2{font-size:1.05rem;margin:28px 0 10px}
.sub{color:var(--mut);margin:0 0 16px}.meta{color:var(--mut);font-size:.85rem;margin-top:28px;border-top:1px solid var(--line);padding-top:12px;overflow-wrap:anywhere}
.counts{display:flex;gap:8px;flex-wrap:wrap;margin:12px 0 4px}.pill{border-radius:999px;padding:3px 12px;font-size:.9rem;font-weight:600}
.create{color:var(--add);background:var(--addbg)}.update{color:var(--upd);background:var(--updbg)}.delete{color:var(--del);background:var(--delbg)}
.warn{background:var(--warnbg);color:var(--warn);border:1px solid var(--line);border-radius:10px;padding:10px 14px;margin:14px 0}.warn ul{margin:6px 0 0;padding-left:20px}
details{border:1px solid var(--line);border-radius:10px;margin:10px 0;background:var(--card)}
summary{cursor:pointer;padding:12px 14px;display:flex;gap:10px;align-items:center;flex-wrap:wrap;list-style:none}summary::-webkit-details-marker{display:none}
summary:focus-visible{outline:2px solid var(--upd);outline-offset:2px;border-radius:10px}
.name{font-weight:600;overflow-wrap:anywhere}.type,.dep{color:var(--mut);font-size:.85rem}.n{margin-left:auto;color:var(--mut);font-size:.85rem;white-space:nowrap}
.body{padding:0 14px 14px}.tbl{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:.88rem}
th,td{text-align:left;vertical-align:top;padding:6px 8px;border-top:1px solid var(--line)}th{color:var(--mut);font-weight:600;white-space:nowrap}
code{font:0.85rem ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;overflow-wrap:anywhere}
td.b code{color:var(--del)}td.a code{color:var(--add)}.none{color:var(--mut);font-style:italic}.flag{color:var(--warn);font-size:.8rem;display:block;margin-top:2px}
</style></head><body><main>
<h1>{{heading .}}</h1>
<p class="sub">{{summary .}}{{if .Description}} {{.Description}}{{end}}</p>
<div class="counts">{{with counts .}}{{if .C}}<span class="pill create">{{.C}} created</span>{{end}}{{if .U}}<span class="pill update">{{.U}} updated</span>{{end}}{{if .D}}<span class="pill delete">{{.D}} deleted</span>{{end}}{{end}}</div>
{{if .Warnings}}<div class="warn" role="note"><strong>Look at these first</strong><ul>{{range .Warnings}}<li>{{.}}</li>{{end}}</ul></div>{{end}}
<h2>What changes in {{.Target.Name}}</h2>
{{$t := .Target.Name}}{{range .Items}}
<details{{if and (eq .Action "update") (or .Selected .Flagged)}} open{{end}}>
<summary><span class="pill {{.Action}}">{{upper .Action}}</span><span class="name">{{.Name}}</span><span class="type">{{.Type}}</span>{{if not .Selected}}<span class="dep">dependency, added by PingOne</span>{{end}}<span class="n">{{if eq .Action "update"}}{{len .Changes}} field{{if ne (len .Changes) 1}}s{{end}}{{else}}{{.Fields}} fields{{end}}</span></summary>
<div class="body">
{{if eq .Action "create"}}<p>New in {{$t}}. Nothing exists there under this name, so every field comes from the source.</p>
{{else if eq .Action "delete"}}<p>Removed from {{$t}}.</p>
{{else if not .Changes}}<p>No field differs.</p>
{{else}}<div class="tbl"><table><thead><tr><th scope="col">Field</th><th scope="col">Now in {{$t}}</th><th scope="col">After</th></tr></thead><tbody>
{{range .Changes}}<tr><td><code>{{.Path}}</code></td><td class="b">{{if eq .Kind "add"}}<span class="none">not set</span>{{else}}<code>{{.Before}}</code>{{end}}</td><td class="a">{{if eq .Kind "remove"}}<span class="none">removed</span>{{else}}<code>{{.After}}</code>{{end}}{{if .Note}}<span class="flag">⚠ {{.Note}}</span>{{end}}</td></tr>
{{end}}</tbody></table></div>{{end}}
</div></details>{{end}}
<p class="meta">Promotion {{.PromotionID}} · plan digest {{short .Digest}}… · source {{.Source.Name}} ({{.Source.ID}}) · target {{.Target.Name}} ({{.Target.ID}})<br>The apply job runs this promotion only while its operations still have this digest. Secret values are never shown.</p>
</main></body></html>
`))

// HTML is the full report kept as a job artifact.
func HTML(r Review) ([]byte, error) {
	var b bytes.Buffer
	if err := page.Execute(&b, r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
