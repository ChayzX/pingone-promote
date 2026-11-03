"""Generate the PingOne Promote DaVinci app flow export from fn/*.js.

usage: python3 gen_app.py [--out FILE] [--values values.json]

Writes flow/pingone-promote-app.flow.json by default: one main flow in the
multi-flow export wrapper ({"flows": [...], "companyId", "customerId"}), the
shape the feasibility flow (../davinci-feasibility) and the prior iteration
imported. The main flow calls YOUR PingOne SSO sign-in subflow first (see
DESIGN.md, "Sign-in subflow"); point that node at it after import.

The app is a hub: Render screen -> App screen -> Handle input -> (a REST call
and its handler, looping while there is work) -> Render screen. Every custom
function carries fn/_contract.js as `contract` and fn/_lib.js as `lib` plus
one step body, exactly as test_app.js rebuilds them.

values.json (optional, keep it private) may set your DaVinci connection IDs,
the sign-in subflow ID and non-identifying presets. Environment IDs, the
GitLab project and all secrets are never baked in: they are set in DaVinci.
"""
import argparse
import json
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
FN = HERE / "fn"
DEFAULT_OUT = HERE / "flow" / "pingone-promote-app.flow.json"

COMPANY = "00000000-0000-0000-0000-000000000000"  # DaVinci replaces it on import
CUSTOMER = "00000000000000000000000000000000"
FLOW_ID = "f0d00a7e00000000000000000000fe10"
SIGNIN_FLOW_PLACEHOLDER = "f0d00a7e00000000000000000000fe11"  # your sign-in subflow, set after import
# Placeholder connection IDs; point the nodes at your connections after import.
CONN = {
    "http": ["httpConnector", "a1f0d00a000000000000000000000002", "Http"],
    "fn": ["functionsConnector", "a1f0d00a000000000000000000000003", "Functions"],
    "var": ["variablesConnector", "a1f0d00a000000000000000000000005", "Variables"],
    "flow": ["flowConnector", "a1f0d00a000000000000000000000006", "Flow Conductor"],
}
CONNECTION_KEYS = {"httpConnectionId": "http", "functionsConnectionId": "fn", "variablesConnectionId": "var", "flowConnectionId": "flow"}

# Flow variables (Flow -> Variables), named pp<Setting>. IDs and the group list are empty on purpose.
SETTINGS = {
    "apiOrigin": "https://api.pingone.com",
    "authOrigin": "https://auth.pingone.com",
    "workerEnvironmentId": "",
    "sourceEnvs": "",
    "pathFlows": "/v1/environments/{envId}/flows",
    "flowsListQuery": "attributes=id,name",
    "rolloutEnvs": "qa,stage,prod",
    "gitlabApi": "https://gitlab.com/api/v4",
    "gitlabProjectId": "",
    "defaultBranch": "main",
    "applyJobPattern": "apply:{env}",
    "environmentPattern": "{env}",
    "mrLabel": "pingone-promote",
    "allowedGroups": "",
    "runTimeoutSeconds": "600",
    "pipelineAppearSeconds": "120",
    "statusRefreshSeconds": "30",
    "statusMaxMinutes": "30",
    "childPipelines": "yes",
}
EMPTY_SETTINGS = ("workerEnvironmentId", "sourceEnvs", "gitlabProjectId", "allowedGroups")
PRESETTABLE = {k for k in SETTINGS if k not in EMPTY_SETTINGS}
COMPANY_SECRETS = {
    "ppWorkerBasicAuth": "PingOne Promote: read-only DaVinci worker, base64(clientId:clientSecret). Flow search only.",
    "ppGitlabToken": "PingOne Promote: GitLab project access token on the config repo (Developer, api scope).",
}
# Per-run state lives in flow-instance variables, so concurrent users never share it.
INSTANCE_VARS = {"ppState": "object", "ppFlows": "object", "ppUrl": "string", "ppBody": "string", "ppLastFailed": "string"}
# Identity fields the sign-in subflow returns (DESIGN.md, "Sign-in subflow").
IDENTITY = ("userId", "username", "name", "groups")
FLOW_SETTINGS = {"csp": "worker-src 'self' blob:; script-src 'self' https://cdn.jsdelivr.net https://code.jquery.com https://devsdk.singularkey.com http://cdnjs.cloudflare.com 'unsafe-inline' 'unsafe-eval';",
                 "intermediateLoadingScreenCSS": "", "intermediateLoadingScreenHTML": "", "customTimeoutErrorScreenMessage": "",
                 "customTimeoutErrorScreenHTML": "", "customTimeoutErrorScreenCSS": "", "logLevel": 2,
                 "flowTimeoutInSeconds": 1800, "flowHttpTimeoutInSeconds": 15}


def strip(js):
    """Embedded library copy: drop full-line comments and indentation (never touches string contents)."""
    return "\n".join(l.strip() for l in js.replace("\r\n", "\n").split("\n") if l.strip() and not l.strip().startswith("//"))


CONTRACT = strip((FN / "_contract.js").read_text(encoding="utf-8"))
LIB = strip((FN / "_lib.js").read_text(encoding="utf-8"))


def fn_code(step):
    body = (FN / f"{step}.js").read_text(encoding="utf-8").replace("\r\n", "\n")
    return ("module.exports = a = async ({params}) => {\nconst contract = (() => {\n" + CONTRACT + "\nreturn contract;\n})();\n"
            "const lib = (() => {\n" + LIB + "\nreturn lib;\n})();\n// ---- step: " + step + " ----\n" + body + "\n};\n")


def plain(*parts):
    """Plain text with {{references}}, as DaVinci stores a REST call's raw body."""
    return "".join(p if isinstance(p, str) else "{{" + p[1] + "}}" for p in parts)


def slate(*parts):
    children = []
    for p in parts:
        if isinstance(p, str):
            children.append({"text": p})
        else:
            _, ref, label = p
            src = "variable.svg" if ref.startswith("global.") else "http.svg"
            children.extend([{"text": ""}, {"type": "link", "src": src, "url": label, "data": "{{" + ref + "}}",
                                            "tooltip": "{{" + ref + "}}", "children": [{"text": label}]}, {"text": ""}])
    return json.dumps([{"children": children}], indent=2)


def fv(name):
    return ("flow", f"global.flow.variables.{name}", name)


def iv(name):
    return ("instance", f"global.variables.{name}", name)


def cv(name):
    return ("company", f"global.company.variables.{name}", name)


def out(node, path):
    return ("node", f"local.{node}.payload.output.{path}", path.split(".")[-1])


def setting_var(n):
    return "pp" + n[0].upper() + n[1:]


NODE_COMMON = {"group": "nodes", "removed": False, "selected": False, "selectable": True, "locked": False, "grabbable": True, "pannable": False, "classes": ""}
EDGE_COMMON = {"position": {"x": 0, "y": 0}, "group": "edges", "removed": False, "selected": False, "selectable": True, "locked": False,
               "grabbable": True, "pannable": True, "classes": ""}
STD_OUT = {"state": "object", "action": "string", "url": "string"}


class Flow:
    def __init__(self):
        self.nodes, self.edges, self.n = [], [], 0
        self.rest_nodes = set()
        self.x, self.y = 100, 200

    def _id(self, kind):
        self.n += 1
        return f"p{kind}{self.n:08d}"

    def _place(self):
        self.x += 170
        if self.x > 3000:
            self.x, self.y = 270, self.y + 240
        return {"x": self.x, "y": self.y}

    def node(self, conn, capability, props, title, ntype="trigger"):
        nid = self._id("n")
        connector, connection, cname = CONN[conn]
        props = dict(props)
        props["nodeTitle"] = {"value": title}
        self.nodes.append({"data": {"id": nid, "nodeType": "CONNECTION", "connectionId": connection, "connectorId": connector, "name": cname,
                                    "label": cname, "status": "configured", "capabilityName": capability, "type": ntype, "properties": props},
                           "position": self._place(), **NODE_COMMON})
        return nid

    def title(self, nid):
        return next(n["data"]["properties"]["nodeTitle"]["value"] for n in self.nodes if n["data"]["id"] == nid)

    def link(self, source, dest, false_dest=None):
        """source -> evaluator -> dest (true) [and false_dest]. A REST call's
        false branch (non-2xx) notes the step and continues to the same
        handler, which reads the status code."""
        if source in self.rest_nodes and false_dest is None:
            false_dest = self.save(self.title(source) + ": call failed, continue", [("ppLastFailed", "string", self.title(source))])
            self.link(false_dest, dest)
        eid = self._id("e")
        data = {"id": eid, "nodeType": "EVAL", "label": "Evaluator"}
        if false_dest:
            data["properties"] = {false_dest: {"value": "allTriggersFalse"}}
        self.nodes.append({"data": data, "position": self._place(), **NODE_COMMON})
        for a, b in [(source, eid), (eid, dest)] + ([(eid, false_dest)] if false_dest else []):
            self.edges.append({"data": {"id": self._id("g"), "source": a, "target": b}, **EDGE_COMMON})

    def rest(self, title, method, headers, raw=None):
        props = {"url": {"value": slate(iv("ppUrl"))}, "httpMethod": {"value": method},
                 "headers": {"value": [{"key": k, "value": slate(*v)} for k, v in headers]}}
        if raw is not None:
            props["httpBody"] = {"value": "raw"}
            props["raw"] = {"value": plain(*raw)}
        nid = self.node("http", "makeRestApiCall", props, title)
        self.rest_nodes.add(nid)
        return nid

    def fn(self, title, step, inputs, outputs):
        return self.node("fn", "customFunction", {
            "variableInputList": {"value": [{"name": n, "preferredDataType": t, "value": slate(*r) if isinstance(r, list) else slate(r)}
                                           for n, t, r in inputs]},
            "code": {"value": fn_code(step)},
            "outputSchema": {"value": json.dumps({"type": "object", "properties": {k: {"type": v} for k, v in outputs.items()}}, indent=2)},
        }, title)

    def equals(self, title, left, text):
        return self.node("fn", "AEqualsB", {"leftValueA": {"value": slate(left)}, "rightValueB": {"value": slate(text)}}, title)

    def save(self, title, assignments):
        items = []
        for i, (name, vtype, value) in enumerate(assignments):
            assert name in INSTANCE_VARS, name
            items.append({"name": name, "value": slate(*value) if isinstance(value, list) else slate(value),
                          "key": round(0.1 + i / 100, 4), "label": f"{name} ({vtype} - flowInstance)", "type": vtype, "nameDefault": None})
        return self.node("var", "saveValue", {"saveVariables": {"value": items}}, title)

    def page(self, title, html, fields, script=""):
        props = {"customHTML": {"value": html}, "customCSS": {"value": CSS},
                 "formFieldsList": {"value": [{"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
                                               "displayName": f, "hashedVisibility": False} for f in fields]},
                 "validationRules": {"value": []}}
        if script:
            props["customScript"] = {"value": script}
        return self.node("http", "customHTMLTemplate", props, title)

    def subflow(self, title):
        return self.node("flow", "startUiSubFlow", {"subFlowId": {"value": {"label": "Your PingOne SSO sign-in subflow", "value": SIGNIN_FLOW_PLACEHOLDER}},
                                                     "subFlowVersionId": {"value": -1}}, title)

    def route(self, fnode, routes, default, extra=()):
        """Keep the function's state, then branch on its action: each (value, dest) in turn, else default."""
        sv = self.save(self.title(fnode) + ": keep state", [("ppState", "object", out(fnode, "state")), ("ppUrl", "string", out(fnode, "url"))] + list(extra))
        self.link(fnode, sv)
        if not routes:
            self.link(sv, default)
            return
        eqs = [self.equals(f"{self.title(fnode)}: {value}?", out(fnode, "action"), value) for value, _ in routes]
        self.link(sv, eqs[0])
        for i, (value, dest) in enumerate(routes):
            self.link(eqs[i], dest, false_dest=eqs[i + 1] if i + 1 < len(routes) else default)

    def export(self):
        variables = []
        for name in sorted(SETTINGS):
            vname = setting_var(name)
            variables.append({"context": "flow", "fields": {"type": "string", "displayName": vname, "value": VALUES_SETTINGS.get(name, SETTINGS[name]),
                                                            "mutable": True, "min": 0, "max": 2000},
                              "flowId": FLOW_ID, "type": "property", "visibility": "private", "name": f"{vname}##SK##flow##SK##{FLOW_ID}", "companyId": COMPANY})
        for name, display in COMPANY_SECRETS.items():
            variables.append({"context": "company", "fields": {"type": "secret", "displayName": display, "value": "", "mutable": True, "min": 0, "max": 2000},
                              "type": "property", "visibility": "private", "name": f"{name}##SK##company", "companyId": COMPANY})
        for name, vtype in sorted(INSTANCE_VARS.items()):
            variables.append({"context": "flowInstance", "fields": {"type": vtype, "displayName": "", "mutable": True, "min": 0, "max": 2000},
                              "type": "property", "visibility": "private", "name": f"{name}##SK##flowInstance", "companyId": COMPANY})
        flow = {
            "companyId": COMPANY, "authTokenExpireIds": [], "connectorIds": sorted({n["data"]["connectorId"] for n in self.nodes if n["data"]["nodeType"] == "CONNECTION"}),
            "createdDate": 1793232000000, "currentVersion": 1, "customerId": CUSTOMER,
            "description": "PingOne Promote request app: PingOne SSO sign-in, source env and flow search, preview, submit and rollout status. "
                           "Writes only .pingone-promote/request.json on a pingone-promote/<requestId> branch; the GitLab request job does the export. See davinci-app/DESIGN.md.",
            "flowStatus": "enabled", "isOutputSchemaSaved": False, "name": "PingOne Promote - request app", "publishedVersion": 1,
            "timeouts": "null", "updatedDate": 1793232000000, "flowId": FLOW_ID, "versionId": 1,
            "graphData": {"elements": {"nodes": self.nodes, "edges": self.edges}, "data": {}, "zoomingEnabled": True, "userZoomingEnabled": True,
                          "zoom": 1, "minZoom": 1e-50, "maxZoom": 1e50, "panningEnabled": True, "userPanningEnabled": True, "pan": {"x": 0, "y": 0},
                          "boxSelectionEnabled": True, "renderer": {"name": "null"}},
            "settings": FLOW_SETTINGS, "flowColor": "#E3F0FF", "savedDate": 1793232000000, "variables": variables, "connections": [],
            "parentFlowId": FLOW_ID,
        }
        return {"flows": [flow], "companyId": COMPANY, "customerId": CUSTOMER}


# ---------------- screens ------------------------------------------------------
CSS = """
.pp{--pp-bg:#f2f4f7;--pp-card:#fff;--pp-ink:#16202c;--pp-muted:#4f5b69;--pp-line:#cdd4dc;--pp-bar:#0f2744;--pp-accent:#0a58a8;
--pp-ok:#11603a;--pp-ok-soft:#eaf6ef;--pp-warn:#7a4f00;--pp-warn-soft:#fff4d9;--pp-bad:#9b1419;--pp-bad-soft:#fcefef;
font:15px/1.55 "Segoe UI",system-ui,-apple-system,Roboto,Arial,sans-serif;color:var(--pp-ink);background:var(--pp-bg);min-height:100vh;box-sizing:border-box;text-align:left}
.pp *{box-sizing:border-box}
.pp-bar{background:var(--pp-bar);color:#fff;padding:0 24px;height:48px;display:flex;align-items:center;font-weight:600}
.pp-nav{max-width:1080px;margin:16px auto 0;padding:0 16px;display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.pp-user{margin-left:auto;color:var(--pp-muted);font-size:13px}
.pp-main{max-width:1080px;margin:12px auto 32px;padding:22px 24px;background:var(--pp-card);border:1px solid var(--pp-line);border-radius:6px}
.pp h1{font-size:22px;margin:0 0 6px}.pp h2{font-size:16px;margin:24px 0 8px}.pp h3{font-size:14px;margin:12px 0 6px}
.pp h1:focus{outline:none}
.pp-lead{color:var(--pp-muted);margin:0 0 14px}.pp-hint{color:var(--pp-muted);font-size:13px;margin:4px 0}
.pp-sum{display:grid;grid-template-columns:max-content 1fr;gap:2px 16px;margin:8px 0 14px;font-size:14px}.pp-sum dt{color:var(--pp-muted)}.pp-sum dd{margin:0}
.pp-field{margin:12px 0}.pp label,.pp legend{display:block;font-weight:600;font-size:14px;margin:0 0 4px}
.pp input[type=text],.pp select,.pp textarea{width:100%;max-width:640px;border:1px solid #8a96a3;border-radius:4px;padding:7px 10px;font:14px system-ui,sans-serif;color:var(--pp-ink);background:#fff}
.pp-field.has-err input{border:2px solid var(--pp-bad)}.pp-err{color:var(--pp-bad);font-weight:600;font-size:13px;margin:2px 0}
.pp fieldset{border:1px solid var(--pp-line);border-radius:4px;margin:10px 0;padding:8px 12px}
.pp-radios label,.pp-check{font-weight:400;display:flex;gap:8px;align-items:center;margin:4px 0}
.pp-actions{display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:16px 0 4px}
.pp-btn{appearance:none;border:1px solid var(--pp-accent);background:var(--pp-accent);color:#fff;border-radius:4px;padding:8px 16px;font:600 14px system-ui,sans-serif;cursor:pointer;min-height:36px}
.pp-btn.ghost{background:#fff;color:var(--pp-accent)}.pp-btn.danger{color:var(--pp-bad);border-color:var(--pp-bad)}
.pp-btn[disabled]{background:#e4e8ec;color:#5b6672;border-color:#b5bec8;cursor:not-allowed}
.pp-btn:focus-visible,.pp a:focus-visible,.pp input:focus-visible,.pp select:focus-visible,.pp textarea:focus-visible{outline:3px solid #f2b705;outline-offset:2px}
.pp-why{font-size:13px;color:var(--pp-muted);max-width:420px}
.pp-table{width:100%;border-collapse:collapse;font-size:13px;margin:8px 0}
.pp-table th{background:#f5f7f9;text-align:left;font-size:12px;color:var(--pp-muted);padding:8px 10px;border-bottom:1px solid var(--pp-line)}
.pp-table td{padding:7px 10px;border-bottom:1px solid #e6eaee;vertical-align:top}
.pp code{font:12.5px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:#eef2f6;border-radius:3px;padding:0 4px;word-break:break-all}
.pp .add{color:var(--pp-ok)}.pp .chg{color:var(--pp-accent)}.pp .warn{color:var(--pp-warn)}.pp .bad{color:var(--pp-bad)}.pp .same{color:var(--pp-muted)}
.pp-tag{font-size:11px;border:1px solid var(--pp-line);border-radius:10px;padding:0 6px;color:var(--pp-muted)}
.pp-note{border-radius:4px;padding:10px 14px;margin:10px 0;background:#e9f1fa;border-left:4px solid var(--pp-accent);color:var(--pp-ink)}
.pp-note.ok{background:var(--pp-ok-soft);border-left-color:var(--pp-ok)}.pp-note.warn{background:var(--pp-warn-soft);border-left-color:var(--pp-warn)}
.pp-note.bad{background:var(--pp-bad-soft);border-left-color:var(--pp-bad)}.pp-note p{margin:4px 0}
.pp-empty{color:var(--pp-muted);font-style:italic}
.pp-strip{list-style:none;display:flex;flex-wrap:wrap;gap:8px;padding:0;margin:14px 0}
.pp-chip{border:1px solid var(--pp-line);border-radius:6px;padding:8px 12px;min-width:150px;display:flex;flex-direction:column;background:#fff}
.pp-chip .pp-env{font-weight:700}.pp-chip .pp-detail{font-size:12px;color:var(--pp-muted)}
.pp-chip.live{border-color:var(--pp-ok);background:var(--pp-ok-soft)}.pp-chip.failed{border-color:var(--pp-bad);background:var(--pp-bad-soft)}
.pp-chip.needs-approval{border-color:var(--pp-warn);background:var(--pp-warn-soft)}.pp-chip.running{border-color:var(--pp-accent)}.pp-chip.src{border-style:dashed;color:var(--pp-muted)}
.pp-wait{display:flex;align-items:center;gap:14px}
.pp-spin{width:22px;height:22px;border:3px solid var(--pp-line);border-top-color:var(--pp-accent);border-radius:50%;animation:pp-spin .8s linear infinite}
@keyframes pp-spin{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){.pp-spin{animation:none}}
.pp-big{font-size:18px;font-weight:600}
.pp-sr{position:absolute!important;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.pp-list li{margin:4px 0}
"""


def shell(body):
    return '<div class="pp"><div class="pp-bar">PingOne Promote</div>' + body + "</div>"


def app_html(render_node):
    return shell('<form id="ppMain" data-id="ppMain" novalidate>{{local.' + render_node + '.payload.output.html}}'
                 '<input type="text" class="pp-sr" id="ppForm" name="ppForm" data-id="ppForm-input" tabindex="-1" aria-hidden="true" autocomplete="off" value="{}" />'
                 "</form>")


def stop_html(signin_node):
    return shell('<main class="pp-main">{{local.' + signin_node + '.payload.output.html}}</main>')


LEFT_HTML = shell('<main class="pp-main"><h1>You have left PingOne Promote</h1><p class="pp-lead">Requests you made keep going in GitLab. '
                  "To end your PingOne session as well, sign out of PingOne. Close this tab when you are done.</p></main>")

# The App screen's Script field (plain JavaScript, as Ping's flows store it).
APP_SCRIPT = """// pp-app: gather the screen's inputs into ppForm, move focus, and run auto checks.
(function () {
  var out = document.getElementById('ppForm');
  function collect() {
    var data = {};
    var els = document.querySelectorAll('[data-pp]');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      var path;
      try { path = JSON.parse(el.getAttribute('data-pp')); } catch (e) { continue; }
      if (el.type === 'radio' && !el.checked) { continue; }
      var v = el.type === 'checkbox' ? el.checked : el.value;
      var o = data;
      for (var k = 0; k < path.length - 1; k++) { o[path[k]] = o[path[k]] || {}; o = o[path[k]]; }
      o[path[path.length - 1]] = v;
    }
    if (out) { out.value = JSON.stringify(data); }
  }
  // #57: an edit turns Submit back on when only these options blocked it. The server checks again.
  function edited(e) {
    collect();
    if (!e.target || !e.target.hasAttribute || !e.target.hasAttribute('data-pp')) { return; }
    var bs = document.querySelectorAll('[data-pp-enable-on-edit]');
    for (var i = 0; i < bs.length; i++) {
      bs[i].disabled = false;
      bs[i].removeAttribute('aria-disabled');
      var why = document.getElementById(bs[i].getAttribute('data-pp-enable-on-edit'));
      if (why) { why.textContent = 'Options changed. Preview again, or submit.'; }
    }
  }
  document.addEventListener('input', edited, true);
  document.addEventListener('change', edited, true);
  document.addEventListener('click', collect, true);
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && e.target && e.target.tagName === 'INPUT' && e.target.type === 'text') {
      e.preventDefault();
      var b = document.querySelector('[data-skbuttonvalue="SEARCH"]') || document.querySelector('[data-skbuttonvalue="PREVIEW"]:not([disabled])');
      if (b) { collect(); b.click(); }
    }
  }, true);
  collect();
  var f = document.querySelector('[data-pp-focus]');
  if (f) { f.setAttribute('tabindex', '-1'); f.focus(); }
  var auto = document.getElementById('pp-auto');
  if (!auto) { return; }
  var value = auto.getAttribute('data-value');
  var due = Date.now() + Number(auto.getAttribute('data-delay') || 10000);
  function fire() {
    if (document.hidden) { return; }
    var b = document.querySelector('[data-skbuttonvalue="' + value + '"]');
    if (!b || b.getAttribute('data-pp-sent')) { return; }
    b.setAttribute('data-pp-sent', '1');
    collect();
    b.click();
  }
  setTimeout(fire, Math.max(0, due - Date.now()));
  document.addEventListener('visibilitychange', function () { if (!document.hidden && Date.now() >= due) { fire(); } });
})();
"""


def build():
    f = Flow()
    sub = f.subflow("Sign in with PingOne SSO (your sign-in subflow)")
    settings_in = [(n, "string", fv(setting_var(n))) for n in SETTINGS]
    signin = f.fn("Check sign-in and settings", "signin", [(k, "string", out(sub, k)) for k in IDENTITY] + settings_in, dict(STD_OUT, html="string"))
    f.link(sub, signin)
    stop = f.page("Cannot continue", stop_html(signin), [])
    render = f.fn("Render screen", "render", [("state", "object", iv("ppState"))], {"state": "object", "html": "string"})
    f.route(signin, [("ok", render)], stop, [("ppFlows", "object", "{}"), ("ppBody", "string", "")])
    keep = f.save("Render screen: keep state", [("ppState", "object", out(render, "state"))])
    f.link(render, keep)
    page = f.page("App screen", app_html(render), ["buttonValue", "ppForm"], script=APP_SCRIPT)
    f.link(keep, page)
    handle = f.fn("Handle input", "handle", [("state", "object", iv("ppState")), ("flows", "object", iv("ppFlows")),
                                             ("buttonValue", "string", out(page, "buttonValue")), ("form", "string", out(page, "ppForm"))],
                  dict(STD_OUT, body="string"))
    f.link(page, handle)
    state = ("state", "object", iv("ppState"))
    rest_in = lambda r: [("statusCode", "number", out(r, "rawResponse.statusCode")), ("body", "object", out(r, "rawResponse.body"))]

    # PingOne: worker token, then the flow list (id and name only).
    token = f.rest("PingOne: worker token", "POST", [("Authorization", ["Basic ", cv("ppWorkerBasicAuth")]), ("Content-Type", ["application/x-www-form-urlencoded"])],
                   raw=["grant_type=client_credentials"])
    listr = f.rest("PingOne: list flows (id, name)", "GET", [("Authorization", ["Bearer ", out(token, "rawResponse.body.access_token")]), ("Accept", ["application/json"])])
    # GitLab: one project access token, three calls.
    gl = [("PRIVATE-TOKEN", [cv("ppGitlabToken")]), ("Accept", ["application/json"])]
    commit = f.rest("GitLab: commit request file", "POST", gl + [("Content-Type", ["application/json"])], raw=[iv("ppBody")])
    read = f.rest("GitLab: read", "GET", gl)
    delete = f.rest("GitLab: delete request branch", "DELETE", gl)
    left = f.page("Left the app", LEFT_HTML, [])
    f.route(handle, [("token", token), ("commit", commit), ("read", read), ("signout", left)], render, [("ppBody", "string", out(handle, "body"))])

    p1tok = f.fn("PingOne: token received?", "p1_token", [state] + rest_in(token), STD_OUT)
    f.link(token, p1tok)
    f.route(p1tok, [("list", listr)], render)
    p1list = f.fn("PingOne: collect flows and search", "p1_list", [state, ("flows", "object", iv("ppFlows"))] + rest_in(listr), dict(STD_OUT, flows="object"))
    f.link(listr, p1list)
    f.route(p1list, [("list", listr)], render, [("ppFlows", "object", out(p1list, "flows"))])
    glc = f.fn("GitLab: request committed?", "gl_commit", [state] + rest_in(commit), STD_OUT)
    f.link(commit, glc)
    f.route(glc, [], render)
    glr = f.fn("GitLab: handle read", "gl_read", [state] + rest_in(read), STD_OUT)
    f.link(read, glr)
    f.route(glr, [("read", read), ("delete", delete)], render)
    gld = f.fn("GitLab: branch deleted?", "gl_delete", [state] + rest_in(delete), STD_OUT)
    f.link(delete, gld)
    f.route(gld, [], render)
    return f


def main():
    global VALUES_SETTINGS, SIGNIN_FLOW_PLACEHOLDER
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(DEFAULT_OUT))
    ap.add_argument("--values")
    a = ap.parse_args()
    VALUES_SETTINGS = {}
    if a.values:
        vals = json.loads(pathlib.Path(a.values).read_text(encoding="utf-8"))
        unknown = set(vals) - PRESETTABLE - set(CONNECTION_KEYS) - {"signinFlowId"}
        if unknown:
            sys.exit(f"values file: unknown or not presettable keys {sorted(unknown)} (IDs and secrets are set in DaVinci, not here)")
        for k, slot in list(CONNECTION_KEYS.items()) + [("signinFlowId", None)]:
            cid = str(vals.get(k, "")).strip()
            if cid:
                if not re.fullmatch(r"[0-9a-fA-F]{32}", cid):
                    sys.exit(f"{k} must be a 32-character hex DaVinci ID")
                if slot:
                    CONN[slot][1] = cid.lower()
                else:
                    SIGNIN_FLOW_PLACEHOLDER = cid.lower()
        VALUES_SETTINGS = {k: str(v) for k, v in vals.items() if k in PRESETTABLE}
    data = build().export()
    outp = pathlib.Path(a.out)
    outp.parent.mkdir(parents=True, exist_ok=True)
    outp.write_text(json.dumps(data, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    fl = data["flows"][0]
    nodes = fl["graphData"]["elements"]["nodes"]
    steps = sum(1 for n in nodes if n["data"]["nodeType"] == "CONNECTION")
    print(f"{outp.name}: {steps} steps, {len(nodes) - steps} evaluators, {len(fl['graphData']['elements']['edges'])} edges, {outp.stat().st_size} bytes")


VALUES_SETTINGS = {}
if __name__ == "__main__":
    main()
