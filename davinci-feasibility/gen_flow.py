"""Generate the DaVinci feasibility-test flow export from fn/*.js.

usage: python3 gen_flow.py [--out FILE] [--values values.json] [--trigger TYPE]

Writes flow/pingone-promote-feasibility.flow.json by default: one flow in the
multi-flow export wrapper ({"flows": [...], "companyId", "customerId"}) that the
prior iteration imported successfully. Node shapes follow that live-confirmed
export and Ping's own exports (pingcli-plugin-terraformer tests/testdata/flows).

Each custom function carries fn/_lib.js as `lib` (and fn/_translate.js as `tr`
where needed) plus one step body, exactly as test_functions.js rebuilds them.

values.json (optional, keep it private) may set your DaVinci connection IDs
and non-identifying presets. Environment IDs, the GitLab project and all
secrets are never baked in: they are set in DaVinci after import.
"""
import argparse
import json
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
FN = HERE / "fn"
DEFAULT_OUT = HERE / "flow" / "pingone-promote-feasibility.flow.json"

COMPANY = "00000000-0000-0000-0000-000000000000"  # DaVinci replaces it on import
CUSTOMER = "00000000000000000000000000000000"
FLOW_ID = "f0d00a7e00000000000000000000fe01"
# Placeholder connection IDs; point the nodes at your connections after import.
CONN = {
    "http": ["httpConnector", "a1f0d00a000000000000000000000002", "Http"],
    "fn": ["functionsConnector", "a1f0d00a000000000000000000000003", "Functions"],
    "var": ["variablesConnector", "a1f0d00a000000000000000000000005", "Variables"],
}
CONNECTION_KEYS = {"httpConnectionId": "http", "functionsConnectionId": "fn", "variablesConnectionId": "var"}

# Flow variables the operator sets after import (Flow -> Variables). IDs are empty on purpose.
SETTINGS = {
    "apiOrigin": "https://api.pingone.com",
    "authOrigin": "https://auth.pingone.com",
    "workerEnvironmentId": "",
    "sourceEnvironmentId": "",
    # Endpoint paths are settings so they can be corrected in a live tenant without regenerating.
    "pathFlows": "/v1/environments/{envId}/flows",
    "flowsListQuery": "attributes=id,name",
    "pathFlow": "/v1/environments/{envId}/flows/{flowId}",
    "pathConnectorInstances": "/v1/environments/{envId}/connectorInstances",
    "pathVariables": "/v1/environments/{envId}/variables",
    "gitlabApi": "https://gitlab.com/api/v4",
    "gitlabProjectId": "",
    # What the operator actually set in Flow settings (DaVinci keeps its defaults on import).
    "flowTimeoutSeconds": "300",
    "httpTimeoutSeconds": "15",
    "calibrationLoops": "10",
}
PRESETTABLE = {k for k in SETTINGS if k not in ("workerEnvironmentId", "sourceEnvironmentId", "gitlabProjectId")}
COMPANY_SECRETS = {
    "fsWorkerBasicAuth": "Feasibility test: read-only worker, base64(clientId:clientSecret)",
    "fsGitlabToken": "Feasibility test: GitLab project token (Developer, api scope)",
}
# Per-run state lives in flow-instance variables, so concurrent runs never share it.
INSTANCE_VARS = {"fsState": "object", "fsFrags": "object", "fsUrl": "string", "fsProgress": "string", "fsTfvars": "string", "fsLastFailed": "string"}
TRANSLATE_STEPS = {"inventory", "fetch", "assemble"}
# DaVinci defaults as in a live export (inline scripts need unsafe-inline). The
# import keeps the tenant's own timeouts; fsFlowTimeoutSeconds records them.
FLOW_SETTINGS = {"csp": "worker-src 'self' blob:; script-src 'self' https://cdn.jsdelivr.net https://code.jquery.com https://devsdk.singularkey.com http://cdnjs.cloudflare.com 'unsafe-inline' 'unsafe-eval';",
                 "intermediateLoadingScreenCSS": "", "intermediateLoadingScreenHTML": "", "customTimeoutErrorScreenMessage": "",
                 "customTimeoutErrorScreenHTML": "", "customTimeoutErrorScreenCSS": "", "logLevel": 3,
                 "flowTimeoutInSeconds": 300, "flowHttpTimeoutInSeconds": 15}


def strip(js):
    """Embedded library copy: drop full-line comments and indentation (never touches string contents)."""
    return "\n".join(l.strip() for l in js.replace("\r\n", "\n").split("\n") if l.strip() and not l.strip().startswith("//"))


LIB = strip((FN / "_lib.js").read_text(encoding="utf-8"))
TR = strip((FN / "_translate.js").read_text(encoding="utf-8"))


def fn_code(step):
    body = (FN / f"{step}.js").read_text(encoding="utf-8").replace("\r\n", "\n")
    code = "module.exports = a = async ({params}) => {\nconst lib = (() => {\n" + LIB + "\nreturn lib;\n})();\n"
    if step in TRANSLATE_STEPS:
        code += "const tr = (() => {\n" + TR + "\nreturn tr;\n})();\n"
    return code + "// ---- step: " + step + " ----\n" + body + "\n};\n"


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
    """A flow-instance variable, read as Ping's own flows do."""
    return ("instance", f"global.variables.{name}", name)


def cv(name):
    return ("company", f"global.company.variables.{name}", name)


def out(node, path):
    return ("node", f"local.{node}.payload.output.{path}", path.split(".")[-1])


def setting_var(n):
    return "fs" + n[0].upper() + n[1:]


NODE_COMMON = {"group": "nodes", "removed": False, "selected": False, "selectable": True, "locked": False, "grabbable": True, "pannable": False, "classes": ""}
EDGE_COMMON = {"position": {"x": 0, "y": 0}, "group": "edges", "removed": False, "selected": False, "selectable": True, "locked": False,
               "grabbable": True, "pannable": True, "classes": ""}
STD_OUT = {"state": "object", "action": "string", "url": "string", "progress": "string"}


class Flow:
    def __init__(self):
        self.nodes, self.edges, self.n = [], [], 0
        self.rest_nodes = set()
        self.x, self.y = 100, 200

    def _id(self, kind):
        self.n += 1
        return f"f{kind}{self.n:08d}"

    def _place(self):
        self.x += 170
        if self.x > 3400:
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

    def link(self, source, target, false_target=None):
        """source -> evaluator -> target (true) [and false_target]. A REST call's
        false branch (failure) notes the step and continues to the same target,
        whose function reads the status code."""
        if source in self.rest_nodes and false_target is None:
            false_target = self.save(self.title(source) + ": call failed, continue", [("fsLastFailed", "string", self.title(source))])
            self.link(false_target, target)
        eid = self._id("e")
        data = {"id": eid, "nodeType": "EVAL", "label": "Evaluator"}
        if false_target:
            data["properties"] = {false_target: {"value": "allTriggersFalse"}}
        self.nodes.append({"data": data, "position": self._place(), **NODE_COMMON})
        for a, b in [(source, eid), (eid, target)] + ([(eid, false_target)] if false_target else []):
            self.edges.append({"data": {"id": self._id("g"), "source": a, "target": b}, **EDGE_COMMON})

    def rest(self, title, method, url, headers, raw=None):
        props = {"url": {"value": slate(*url)}, "httpMethod": {"value": method},
                 "headers": {"value": [{"key": k, "value": slate(*v)} for k, v in headers]}}
        if raw is not None:
            # As DaVinci saves the Body dropdown and raw editor (confirmed by the prior iteration's live export).
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
        """Variables -> Save Value into flow-instance variables (Ping's saveValue/saveVariables shape)."""
        items = []
        for i, (name, vtype, value) in enumerate(assignments):
            assert name in INSTANCE_VARS, name
            items.append({"name": name, "value": slate(*value) if isinstance(value, list) else slate(value),
                          "key": round(0.1 + i / 100, 4), "label": f"{name} ({vtype} - flowInstance)", "type": vtype, "nameDefault": None})
        return self.node("var", "saveValue", {"saveVariables": {"value": items}}, title)

    def page(self, title, html, fields, script="", css=None):
        props = {"customHTML": {"value": html}, "customCSS": {"value": css or CSS},
                 "formFieldsList": {"value": [{"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
                                               "displayName": f, "hashedVisibility": False} for f in fields]},
                 "validationRules": {"value": []}}
        if script:
            props["customScript"] = {"value": script}
        return self.node("http", "customHTMLTemplate", props, title)

    def route(self, fnode, routes, default, extra=()):
        """Keep the function's state, then branch on its action: each (value, target) in turn, else default."""
        sv = self.save(self.title(fnode) + ": keep state", [("fsState", "object", out(fnode, "state")), ("fsUrl", "string", out(fnode, "url")),
                                                            ("fsProgress", "string", out(fnode, "progress"))] + list(extra))
        self.link(fnode, sv)
        prev = sv
        if not routes:
            self.link(sv, default)
            return
        eqs = [self.equals(f"{self.title(fnode)}: {value}?", out(fnode, "action"), value) for value, _ in routes]
        self.link(prev, eqs[0])
        for i, (value, target) in enumerate(routes):
            self.link(eqs[i], target, false_target=eqs[i + 1] if i + 1 < len(routes) else default)

    def export(self, trigger):
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
            "description": "Feasibility test for the pingone-promote workload in DaVinci: read-only PingOne token, inventory, bulk flow fetch, "
                           "in-flow translation to tfvars, branch-only GitLab commit, timed per stage. See README.md (epic #58).",
            "flowStatus": "enabled", "isOutputSchemaSaved": False, "name": "PingOne Promote - DaVinci feasibility test", "publishedVersion": 1,
            "timeouts": "null", "updatedDate": 1793232000000, "flowId": FLOW_ID, "versionId": 1,
            "graphData": {"elements": {"nodes": self.nodes, "edges": self.edges}, "data": {}, "zoomingEnabled": True, "userZoomingEnabled": True,
                          "zoom": 1, "minZoom": 1e-50, "maxZoom": 1e50, "panningEnabled": True, "userPanningEnabled": True, "pan": {"x": 0, "y": 0},
                          "boxSelectionEnabled": True, "renderer": {"name": "null"}},
            "settings": FLOW_SETTINGS, "flowColor": "#E3F0FF", "savedDate": 1793232000000, "variables": variables, "connections": [],
            "parentFlowId": FLOW_ID,
        }
        if trigger:
            flow["trigger"] = {"type": trigger}
        return {"flows": [flow], "companyId": COMPANY, "customerId": CUSTOMER}


# ---------------- screens ------------------------------------------------------
CSS = """
.fs{--fs-bg:#eef1f5;--fs-card:#fff;--fs-ink:#1a2433;--fs-muted:#5a6573;--fs-line:#d8dde4;--fs-bar:#0f2744;--fs-accent:#0b5cad;
--fs-ok:#16683f;--fs-ok-soft:#eef8f2;--fs-warn:#8a5a00;--fs-warn-soft:#fff6e0;--fs-bad:#a4161a;--fs-bad-soft:#fdf1f1;
font:14px/1.55 "Segoe UI",system-ui,-apple-system,Roboto,Arial,sans-serif;color:var(--fs-ink);background:var(--fs-bg);min-height:100vh;
box-sizing:border-box;text-align:left}
.fs *{box-sizing:border-box}
.fs-bar{background:var(--fs-bar);color:#fff;display:flex;align-items:center;justify-content:space-between;padding:0 24px;height:52px;font-weight:600}
.fs-bar small{font-weight:400;color:#b7c4d6;margin-left:10px}
.fs-tag{font-size:11px;letter-spacing:.4px;text-transform:uppercase;color:#ffd58a;border:1px solid #8a6a2e;border-radius:3px;padding:2px 8px}
.fs-main{max-width:1100px;margin:24px auto;padding:0 16px}
.fs-card{background:var(--fs-card);border:1px solid var(--fs-line);border-radius:6px;padding:22px 24px}
.fs h1{font-size:20px;margin:0 0 6px}.fs h2{font-size:14px;margin:22px 0 8px}
.fs-lead{color:var(--fs-muted);margin:0 0 12px}
.fs-table{width:100%;border-collapse:collapse;font-size:13px;margin:8px 0}
.fs-table th{background:#f6f8fa;text-align:left;font-size:12px;color:var(--fs-muted);padding:8px 10px;border-bottom:1px solid var(--fs-line)}
.fs-table td{padding:8px 10px;border-bottom:1px solid #edf0f3;vertical-align:top}
.fs-total td{font-weight:600;border-top:2px solid var(--fs-line)}
.fs .ok{color:var(--fs-ok)}.fs .warn{color:var(--fs-warn)}.fs .bad{color:var(--fs-bad)}
.fs-note{border-radius:4px;padding:10px 14px;margin:10px 0;background:#e8f0fa;border-left:4px solid var(--fs-accent)}
.fs-note.ok{background:var(--fs-ok-soft);border-left-color:var(--fs-ok)}.fs-note.bad{background:var(--fs-bad-soft);border-left-color:var(--fs-bad)}
.fs-verdict{display:inline-flex;align-items:baseline;gap:12px;font-size:28px;font-weight:700;padding:8px 18px;border-radius:6px;margin:4px 0 14px;background:#f6f8fa}
.fs-verdict small{font-size:13px;font-weight:400;color:var(--fs-muted)}
.fs-verdict.ok{background:var(--fs-ok-soft)}.fs-verdict.warn{background:var(--fs-warn-soft)}.fs-verdict.bad{background:var(--fs-bad-soft)}
.fs-checks{margin:0;padding-left:18px}.fs-checks li{margin:3px 0}
.fs label{display:block;font-weight:600;font-size:13px;margin:14px 0 4px}
.fs input[type=text]{width:100%;max-width:520px;border:1px solid #b9c2cd;border-radius:4px;padding:7px 10px;font:13px ui-monospace,Menlo,Consolas,monospace}
.fs-levels{display:flex;gap:10px;flex-wrap:wrap;margin-top:8px}
.fs-btn{appearance:none;border:1px solid var(--fs-accent);background:var(--fs-accent);color:#fff;border-radius:4px;padding:9px 18px;font:600 14px system-ui,sans-serif;cursor:pointer}
.fs-btn.ghost{background:#fff;color:var(--fs-ink);border-color:#b9c2cd}
.fs pre,.fs textarea{width:100%;background:#f6f8fa;border:1px solid #e1e5ea;border-radius:4px;padding:10px 12px;max-height:340px;overflow:auto;
font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre}
.fs code{background:#e8f0fa;border-radius:3px;padding:0 4px;font-size:.92em}
.fs details{margin-top:16px}
.fs-wait{display:flex;align-items:center;gap:14px}
.fs-spin{width:24px;height:24px;border:3px solid var(--fs-line);border-top-color:var(--fs-accent);border-radius:50%;animation:fs-spin .8s linear infinite}
@keyframes fs-spin{to{transform:rotate(360deg)}}
.fs-fallback{margin-top:18px;visibility:hidden;animation:fs-reveal 0s linear 10s forwards}
@keyframes fs-reveal{to{visibility:visible}}
"""


def shell(body):
    return ('<div class="fs"><div class="fs-bar"><span>PingOne Promote<small>DaVinci feasibility test</small></span>'
            '<span class="fs-tag">Read-only test</span></div><div class="fs-main"><div class="fs-card">' + body + "</div></div></div>")


def button(form, value, label, ghost=False):
    return (f'<button type="submit" class="fs-btn{" ghost" if ghost else ""}" data-id="button" data-skcomponent="skbutton" '
            f'data-skbuttontype="form-submit" data-skform="{form}" data-skbuttonvalue="{value}">{label}</button>')


def text_input(name, label, placeholder):
    return (f'<label for="{name}">{label}</label><input type="text" id="{name}" name="{name}" placeholder="{placeholder}" '
            f'autocomplete="off" data-id="{name}-input" />')


def start_html(settings_node):
    return shell(
        "<h1>DaVinci feasibility test</h1>"
        '<p class="fs-lead">Times each stage of the pingone-promote workload inside DaVinci: worker token, inventory, bulk flow fetch, '
        "translation to tfvars and a branch-only GitLab commit. PingOne access is read-only.</p>"
        "<h2>Settings</h2>{{local." + settings_node + ".payload.output.statusHtml}}"
        '<form id="fsForm" data-id="fsForm"><h2>Run options</h2>'
        + text_input("targetName", "Target name (writes environments/&lt;target&gt;.tfvars)", "feasibility")
        + text_input("branchName", "New GitLab branch (leave empty to skip the GitLab stage)", "davinci-feasibility/run-1")
        + text_input("openMr", "Open a merge request? yes / no", "no")
        + text_input("handBackEvery", "Hand back to the browser every N API calls (0 = never)", "10")
        + '<h2>Load level: flows to fetch</h2><div class="fs-levels">'
        + button("fsForm", "N1", "1 flow") + button("fsForm", "N5", "5 flows") + button("fsForm", "N20", "20 flows")
        + button("fsForm", "ALL", "All flows", ghost=True) + "</div></form>")


def wait_html():
    return shell('<h1>Working</h1><div class="fs-wait"><div class="fs-spin"></div><div>{{global.variables.fsProgress}}<br>'
                 '<small class="fs-lead">This page continues by itself (a hand-back to the browser).</small></div></div>'
                 '<form id="waitForm" data-id="waitForm"><div class="fs-fallback">' + button("waitForm", "CONTINUE", "Continue", ghost=True)
                 + "</div></form>")


def result_html(finish_node):
    return shell("<h1>Feasibility test results</h1>{{local." + finish_node + ".payload.output.resultHtml}}"
                 "<h2>Copy results</h2><p class=\"fs-lead\">Paste this JSON back to the pingone-promote team. It holds counts, sizes and "
                 "timings only: no names, IDs, URLs or secrets.</p>"
                 '<textarea id="fsResults" readonly rows="16">{{local.' + finish_node + '.payload.output.resultsText}}</textarea>'
                 '<p><button type="button" id="fsCopy" class="fs-btn">Copy results</button></p>')


# Script fields (plain JavaScript, as Ping's flows store them). One submit per page.
AUTO_CONTINUE = """// fs-auto-continue: submit this working page once, shortly after it renders.
(function () {
  setTimeout(function () {
    var b = document.querySelector('[data-skbuttonvalue="CONTINUE"]');
    if (!b || b.getAttribute('data-fs-sent')) { return; }
    b.setAttribute('data-fs-sent', '1');
    b.click();
  }, 300);
})();
"""
COPY_SCRIPT = """// fs-copy: copy the results JSON to the clipboard.
(function () {
  var b = document.getElementById('fsCopy');
  var t = document.getElementById('fsResults');
  if (!b || !t) { return; }
  b.addEventListener('click', function (e) {
    e.preventDefault();
    t.select();
    var done = function () { b.textContent = 'Copied'; };
    if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(t.value).then(done, function () { document.execCommand('copy'); done(); }); }
    else { document.execCommand('copy'); done(); }
  });
})();
"""


def build():
    f = Flow()
    settings_in = [(n, "string", fv(setting_var(n))) for n in SETTINGS]
    st = f.fn("Check settings", "settings", settings_in, {"ready": "string", "statusHtml": "string"})
    start_page = f.page("Start page", start_html(st), ["buttonValue", "targetName", "branchName", "openMr", "handBackEvery"])
    f.link(st, start_page)
    start = f.fn("Start run", "start", settings_in + [(k, "string", out(start_page, k)) for k in
                                                       ("buttonValue", "targetName", "branchName", "openMr", "handBackEvery")], STD_OUT)
    f.link(start_page, start)
    finish = f.fn("6 Results", "finish", [("state", "object", iv("fsState")), ("tfvars", "string", iv("fsTfvars"))],
                  {"verdict": "string", "resultHtml": "string", "resultsText": "string"})
    result = f.page("Results page", result_html(finish), [], script=COPY_SCRIPT)
    f.link(finish, result)
    state = ("state", "object", iv("fsState"))
    rest_in = lambda r: [("statusCode", "number", out(r, "rawResponse.statusCode")), ("body", "object", out(r, "rawResponse.body"))]

    # 0 calibration, 1 auth
    calib = f.fn("0 Calibrate DaVinci overhead", "calibrate", [state], STD_OUT)
    f.route(start, [("ok", calib)], finish, [("fsFrags", "object", "{}"), ("fsTfvars", "string", "")])
    token = f.rest("1 Worker token (client credentials)", "POST", [iv("fsUrl")],
                   [("Authorization", ["Basic ", cv("fsWorkerBasicAuth")]), ("Content-Type", ["application/x-www-form-urlencoded"])],
                   raw=["grant_type=client_credentials"])
    f.route(calib, [("more", calib), ("token", token)], finish)
    auth = f.fn("1 Token received?", "auth", [state] + rest_in(token), STD_OUT)
    f.link(token, auth)
    bearer = [("Authorization", ["Bearer ", out(token, "rawResponse.body.access_token")]), ("Accept", ["application/json"])]

    # 2 inventory (paged, three collections), 3 fetch + 4 translate, hand-backs
    inv_get = f.rest("2 Inventory: read page", "GET", [iv("fsUrl")], bearer)
    frags = ("frags", "object", iv("fsFrags"))
    inv = f.fn("2 Inventory: collect page", "inventory", [state, frags] + rest_in(inv_get), dict(STD_OUT, frags="object"))
    f.route(auth, [("inventory", inv_get)], finish)
    f.link(inv_get, inv)
    wait = f.page("Working (hand back to the browser)", wait_html(), ["buttonValue"], script=AUTO_CONTINUE)
    resume = f.fn("Resume after hand-back", "resume", [state], STD_OUT)
    fetch_get = f.rest("3 Fetch: get flow", "GET", [iv("fsUrl")], bearer)
    fetch = f.fn("3 Fetch: collect and translate flow", "fetch", [state, frags] + rest_in(fetch_get), dict(STD_OUT, frags="object"))
    assemble = f.fn("4 Translate: tiers, variables, tfvars", "assemble", [state, frags], dict(STD_OUT, tfvars="string"))
    keep_frags = [("fsFrags", "object", out(inv, "frags"))]
    f.route(inv, [("more", inv_get), ("pause", wait), ("fetch", fetch_get), ("assemble", assemble)], finish, keep_frags)
    f.link(wait, resume)
    f.route(resume, [("inventory", inv_get), ("fetch", fetch_get)], finish)
    f.link(fetch_get, fetch)
    f.route(fetch, [("more", fetch_get), ("pause", wait), ("assemble", assemble)], finish, [("fsFrags", "object", out(fetch, "frags"))])

    # 5 GitLab, branch only
    gl = [("PRIVATE-TOKEN", [cv("fsGitlabToken")]), ("Accept", ["application/json"])]
    gl_post = gl + [("Content-Type", ["application/json"])]
    steps = [("5 GitLab: read project", "GET", "gitlab_project", "Project private? default branch"),
             ("5 GitLab: does the branch exist?", "GET", "gitlab_branch", "Branch new?"),
             ("5 GitLab: protected branches", "GET", "gitlab_protected", "Branch unprotected?"),
             ("5 GitLab: file on default branch?", "GET", "gitlab_file", "Build commit")]
    prev, prev_routes = assemble, "gitlab"
    first_extra = [("fsTfvars", "string", out(assemble, "tfvars"))]
    for title, method, step, ftitle in steps:
        r = f.rest(title, method, [iv("fsUrl")], gl)
        f.route(prev, [(prev_routes, r)], finish, first_extra)
        first_extra = []
        ins = [state] + rest_in(r) + ([("tfvars", "string", iv("fsTfvars"))] if step == "gitlab_file" else [])
        outs = dict(STD_OUT, commitBody="string") if step == "gitlab_file" else STD_OUT
        node = f.fn("5 GitLab: " + ftitle, step, ins, outs)
        f.link(r, node)
        prev, prev_routes = node, "ok"
    commit = f.rest("5 GitLab: commit to the new branch", "POST", [iv("fsUrl")], gl_post, raw=[out(prev, "commitBody")])
    f.route(prev, [("ok", commit)], finish)
    committed = f.fn("5 GitLab: committed?", "gitlab_commit", [state] + rest_in(commit), dict(STD_OUT, mrBody="string"))
    f.link(commit, committed)
    mr = f.rest("5 GitLab: open merge request (optional)", "POST", [iv("fsUrl")], gl_post, raw=[out(committed, "mrBody")])
    f.route(committed, [("mr", mr)], finish)
    mr_done = f.fn("5 GitLab: merge request opened?", "gitlab_mr", [state] + rest_in(mr), STD_OUT)
    f.link(mr, mr_done)
    f.route(mr_done, [], finish)
    return f


def main():
    global VALUES_SETTINGS
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(DEFAULT_OUT))
    ap.add_argument("--values")
    ap.add_argument("--trigger", help="optional flow trigger type, e.g. AUTHENTICATION (omitted by default, as in the prior import)")
    a = ap.parse_args()
    VALUES_SETTINGS = {}
    if a.values:
        vals = json.loads(pathlib.Path(a.values).read_text(encoding="utf-8"))
        unknown = set(vals) - PRESETTABLE - set(CONNECTION_KEYS)
        if unknown:
            sys.exit(f"values file: unknown or not presettable keys {sorted(unknown)} (IDs and secrets are set in DaVinci, not here)")
        for k, slot in CONNECTION_KEYS.items():
            cid = str(vals.get(k, "")).strip()
            if cid:
                if not re.fullmatch(r"[0-9a-fA-F]{32}", cid):
                    sys.exit(f"{k} must be a 32-character hex DaVinci connection ID")
                CONN[slot][1] = cid.lower()
        VALUES_SETTINGS = {k: str(v) for k, v in vals.items() if k in PRESETTABLE}
    data = build().export(a.trigger)
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
