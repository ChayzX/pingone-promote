"""Generate the DaVinci front-door flow export (single flow, variables included).

Node shapes follow real DaVinci exports (terraform-provider-davinci acctest
flows): CONNECTION nodes with connectorId/capabilityName/properties, EVAL nodes
between steps, Slate-encoded values with {{...}} link references.
"""
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).parent
FUNCS = HERE / "functions"
OUT = pathlib.Path(sys.argv[1])  # usage: gen_flow.py OUT.json [values.json]

FLOW_ID = "f0d00a7e1c2b4e8d9a3f5b6c7d8e9f01"
COMPANY = "00000000-0000-0000-0000-000000000000"  # replaced by DaVinci on import

# Placeholder connection IDs; map them to your connections on import.
CONN = {
    "oidc": ("genericConnector", "a1f0d00a000000000000000000000001", "PingOne Admin SSO"),
    "http": ("httpConnector", "a1f0d00a000000000000000000000002", "Http"),
    "fn": ("functionsConnector", "a1f0d00a000000000000000000000003", "Functions"),
    "err": ("errorConnector", "a1f0d00a000000000000000000000004", "Error Message"),
}


def slate(*parts):
    """Slate rich-text value. Strings are literal text; tuples are references."""
    children = []
    for p in parts:
        if isinstance(p, str):
            children.append({"text": p})
        else:
            kind, ref, label = p
            src = {"flow": "variable.svg", "company": "variable.svg"}.get(kind, "http.svg")
            children.append({"text": ""})
            children.append({"type": "link", "src": src, "url": label, "data": "{{" + ref + "}}",
                             "tooltip": "{{" + ref + "}}", "children": [{"text": label}]})
            children.append({"text": ""})
    return json.dumps([{"children": children}], indent=2)


def fv(name):
    return ("flow", f"global.flow.variables.{name}", name)


def cv(name):
    return ("company", f"global.company.variables.{name}", name)


def out(node, path, label=None):
    return ("node", f"local.{node}.payload.output.{path}", label or path.split(".")[-1])


nodes, edges = [], []
_pos = {"x": 100, "y": 200}


def add(node_id, conn, capability, props, title, x, y, ntype="trigger"):
    connector_id, connection_id, name = CONN[conn]
    props = dict(props)
    props["nodeTitle"] = {"value": title}
    nodes.append({
        "data": {"id": node_id, "nodeType": "CONNECTION", "connectionId": connection_id,
                 "connectorId": connector_id, "name": name, "label": name, "status": "configured",
                 "capabilityName": capability, "type": ntype, "properties": props},
        "position": {"x": x, "y": y}, "group": "nodes", "removed": False, "selected": False,
        "selectable": True, "locked": False, "grabbable": True, "pannable": False, "classes": ""})


def evaluator(eval_id, x, y, false_targets=()):
    data = {"id": eval_id, "nodeType": "EVAL", "label": "Evaluator"}
    if false_targets:
        data["properties"] = {t: {"value": "allTriggersFalse"} for t in false_targets}
    nodes.append({"data": data, "position": {"x": x, "y": y}, "group": "nodes", "removed": False,
                  "selected": False, "selectable": True, "locked": False, "grabbable": True,
                  "pannable": False, "classes": ""})


def edge(edge_id, source, target):
    edges.append({"data": {"id": edge_id, "source": source, "target": target}, "position": {"x": 0, "y": 0},
                  "group": "edges", "removed": False, "selected": False, "selectable": True,
                  "locked": False, "grabbable": True, "pannable": True, "classes": ""})


_edge_n = [0]


def link(source, eval_id, ex, ey, targets, false_targets=()):
    evaluator(eval_id, ex, ey, false_targets)
    _edge_n[0] += 1
    edge(f"edge{_edge_n[0]:06d}", source, eval_id)
    for t in targets:
        _edge_n[0] += 1
        edge(f"edge{_edge_n[0]:06d}", eval_id, t)


def rest(node_id, title, method, url_parts, headers, x, y, raw_body=None):
    props = {
        "url": {"value": slate(*url_parts)},
        "httpMethod": {"value": method},
        "headers": {"value": [{"key": k, "value": slate(*v)} for k, v in headers]},
    }
    if raw_body is not None:
        props["bodyType"] = {"value": "raw"}
        props["rawBody"] = {"value": slate(*raw_body)}
    add(node_id, "http", "makeRestApiCall", props, title, x, y)


def function(node_id, title, file, inputs, output_schema, x, y):
    code = (FUNCS / file).read_text()
    add(node_id, "fn", "customFunction", {
        "variableInputList": {"value": [
            {"name": n, "preferredDataType": t, "value": slate(ref)} for n, t, ref in inputs]},
        "code": {"value": code},
        "outputSchema": {"value": json.dumps({"type": "object", "properties": output_schema}, indent=2)},
    }, title, x, y)


def equals(node_id, title, left, right_text, x, y):
    add(node_id, "fn", "AEqualsB", {"leftValueA": {"value": slate(left)},
                                    "rightValueB": {"value": slate(right_text)}}, title, x, y)


def error(node_id, title, message, x, y, description=None):
    props = {"errorMessage": {"value": slate(*message)}}
    if description:
        props["errorDescription"] = {"value": slate(*description)}
    add(node_id, "err", "customErrorMessage", props, title, x, y, ntype="action")


def message(node_id, title, parts, x, y):
    add(node_id, "http", "customHtmlMessage", {"message": {"value": slate(*parts)},
                                               "showContinueButton": {"value": False}}, title, x, y, ntype="action")


def page(node_id, title, html, fields, x, y):
    add(node_id, "http", "customHTMLTemplate", {
        "customHTML": {"value": html},
        "formFieldsList": {"value": [
            {"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
             "displayName": f, "hashedVisibility": False} for f in fields]},
        "validationRules": {"value": []},
    }, title, x, y)


STR, OBJ = "string", "object"
S = {"type": "string"}
# --- 1. Sign in (OIDC to the admins' environment) and read who it is ---------
add("signinoidc", "oidc", "loginFirstFactor", {}, "Sign in with PingOne (admin SSO)", 100, 200)
link("signinoidc", "evsignin01", 220, 200, ["userinfoap"])
rest("userinfoap", "Who signed in (userinfo)", "GET",
     ["https://auth.pingone.com/", fv("adminEnvironmentId"), "/as/userinfo"],
     [("Authorization", ["Bearer ", out("signinoidc", "accessToken")])], 340, 200)
link("userinfoap", "evuserin01", 460, 200, ["workertokn"])
rest("workertokn", "Read-only worker token", "POST",
     ["https://auth.pingone.com/", fv("hostEnvironmentId"), "/as/token"],
     [("Authorization", ["Basic ", cv("frontDoorWorkerBasicAuth")]),
      ("Content-Type", ["application/x-www-form-urlencoded"])], 580, 200,
     raw_body=["grant_type=client_credentials"])
link("workertokn", "evworker01", 700, 200, ["rolesgetap"])
rest("rolesgetap", "Signed-in admin's role assignments", "GET",
     ["https://api.pingone.com/v1/environments/", fv("adminEnvironmentId"), "/users/",
      out("userinfoap", "rawResponse.body.sub", "sub"), "/roleAssignments"],
     [("Authorization", ["Bearer ", out("workertokn", "rawResponse.body.access_token", "access_token")])], 820, 200)
link("rolesgetap", "evroles001", 940, 200, ["roleslstap"])
rest("roleslstap", "PingOne role list (find DaVinci Admin)", "GET", ["https://api.pingone.com/v1/roles"],
     [("Authorization", ["Bearer ", out("workertokn", "rawResponse.body.access_token", "access_token")])], 940, 80)
link("roleslstap", "evrolel001", 1000, 120, ["authzcheck"])
function("authzcheck", "Allow only DaVinci Admins (org or source env)", "authz.js", [
    ("userinfoStatus", "number", out("userinfoap", "rawResponse.statusCode", "statusCode")),
    ("userinfoBody", OBJ, out("userinfoap", "rawResponse.body", "body")),
    ("roleAssignmentsStatus", "number", out("rolesgetap", "rawResponse.statusCode", "statusCode")),
    ("roleAssignmentsBody", OBJ, out("rolesgetap", "rawResponse.body", "body")),
    ("rolesListStatus", "number", out("roleslstap", "rawResponse.statusCode", "statusCode")),
    ("rolesListBody", OBJ, out("roleslstap", "rawResponse.body", "body")),
    ("davinciAdminRoleId", STR, fv("davinciAdminRoleId")),
    ("organizationId", STR, fv("organizationId")),
    ("sourceEnvironmentId", STR, fv("sourceEnvironmentId")),
], {"decision": S, "reason": S, "userId": S, "username": S, "usernameText": S}, 1060, 200)
link("authzcheck", "evauthz001", 1180, 200, ["authzallow"])
equals("authzallow", "Decision is allow?", out("authzcheck", "decision"), "allow", 1300, 200)
link("authzallow", "evallow001", 1420, 200, ["appslistap", "deniedpage"], false_targets=["deniedpage"])
error("deniedpage", "Access denied", ["Access denied: ", out("authzcheck", "reason")], 1420, 360)

# --- 2. Show what can be promoted and collect the selection ------------------
rest("appslistap", "List source applications", "GET",
     ["https://api.pingone.com/v1/environments/", fv("sourceEnvironmentId"), "/applications"],
     [("Authorization", ["Bearer ", out("workertokn", "rawResponse.body.access_token", "access_token")])], 1540, 200)
link("appslistap", "evapps0001", 1660, 200, ["appsformat"])
function("appsformat", "Format application list", "listapps.js", [
    ("applicationsStatus", "number", out("appslistap", "rawResponse.statusCode", "statusCode")),
    ("applicationsBody", OBJ, out("appslistap", "rawResponse.body", "body")),
], {"listText": S, "count": {"type": "number"}}, 1780, 200)
link("appsformat", "evfmt00001", 1900, 200, ["selectform"])

SELECT_HTML = """<div class="container py-4" style="max-width: 820px">
  <h1 class="h3 mb-3">Request a PingOne promotion</h1>
  <p class="text-muted">Signed in as <strong>{{local.authzcheck.payload.output.usernameText}}</strong>.
  Promotions go from <code>{{global.flow.variables.sourceEnvironmentId}}</code> to <code>{{global.flow.variables.targetEnvironmentId}}</code>.
  Submitting opens a GitLab merge request; nothing changes in PingOne until the pipeline is approved.</p>
  <h2 class="h6">Applications in the source environment (ID, name)</h2>
  <pre class="border rounded p-2 small" style="max-height: 220px; overflow: auto">{{local.appsformat.payload.output.listText}}</pre>
  <form id="selectionForm" data-id="selectionForm">
    <div class="mb-3">
      <label class="form-label" for="selectedRaw">Resource IDs to promote (one per line or comma-separated)</label>
      <textarea class="form-control" id="selectedRaw" name="selectedRaw" rows="4" data-id="selectedRaw-input"></textarea>
    </div>
    <div class="mb-3">
      <label class="form-label" for="mappingsRaw">Optional mappings, one <code>sourceId=targetId</code> per line</label>
      <textarea class="form-control" id="mappingsRaw" name="mappingsRaw" rows="2" data-id="mappingsRaw-input"></textarea>
    </div>
    <div class="mb-3">
      <label class="form-label" for="description">Why is this change needed?</label>
      <textarea class="form-control" id="description" name="description" rows="3" maxlength="3000" data-id="description-input"></textarea>
    </div>
    <button type="submit" class="btn btn-primary" data-id="button" data-skcomponent="skbutton"
      data-skbuttontype="form-submit" data-skform="selectionForm" data-skbuttonvalue="REVIEW">Review request</button>
  </form>
</div>"""
page("selectform", "Choose resources", SELECT_HTML, ["selectedRaw", "mappingsRaw", "description", "buttonValue"], 2020, 200)
link("selectform", "evselect01", 2140, 200, ["buildinten"])
function("buildinten", "Build intent.json and GitLab bodies", "buildintent.js", [
    ("userId", STR, out("authzcheck", "userId")),
    ("username", STR, out("authzcheck", "username")),
    ("sourceEnvironmentId", STR, fv("sourceEnvironmentId")),
    ("targetEnvironmentId", STR, fv("targetEnvironmentId")),
    ("region", STR, fv("region")),
    ("selectedRaw", STR, out("selectform", "selectedRaw")),
    ("mappingsRaw", STR, out("selectform", "mappingsRaw")),
    ("description", STR, out("selectform", "description")),
    ("defaultBranch", STR, fv("gitlabDefaultBranch")),
], {"status": S, "error": S, "intentId": S, "intentJson": S, "commitBody": S, "mrBody": S, "summaryText": S},
    2260, 200)
link("buildinten", "evbuild001", 2380, 200, ["buildvalid"])
equals("buildvalid", "Intent valid?", out("buildinten", "status"), "ok", 2500, 200)
link("buildvalid", "evvalid001", 2620, 200, ["confirmpge", "invalidreq"], false_targets=["invalidreq"])
error("invalidreq", "Invalid request", ["The request was not submitted: ", out("buildinten", "error")], 2620, 360,
      description=["Start the flow again and correct the selection."])

CONFIRM_HTML = """<div class="container py-4" style="max-width: 820px">
  <h1 class="h3 mb-3">Confirm the promotion request</h1>
  <p class="text-muted">This intent file will be committed to a new branch and a merge request will be opened for review.</p>
  <pre class="border rounded p-2 small" style="max-height: 360px; overflow: auto">{{local.buildinten.payload.output.summaryText}}</pre>
  <form id="confirmForm" data-id="confirmForm">
    <button type="submit" class="btn btn-primary me-2" data-id="button" data-skcomponent="skbutton"
      data-skbuttontype="form-submit" data-skform="confirmForm" data-skbuttonvalue="SUBMIT">Open merge request</button>
    <button type="submit" class="btn btn-outline-secondary" data-id="button" data-skcomponent="skbutton"
      data-skbuttontype="form-submit" data-skform="confirmForm" data-skbuttonvalue="CANCEL">Cancel</button>
  </form>
</div>"""
page("confirmpge", "Confirm request", CONFIRM_HTML, ["buttonValue"], 2740, 200)
link("confirmpge", "evconfrm01", 2860, 200, ["submitchck"])
equals("submitchck", "Submit pressed?", out("confirmpge", "buttonValue"), "SUBMIT", 2980, 200)
link("submitchck", "evsubmit01", 3100, 200, ["gitcommit1", "cancelledm"], false_targets=["cancelledm"])
message("cancelledm", "Cancelled", ["Cancelled. Nothing was submitted."], 3100, 360)

# --- 3. Commit the intent and open the merge request -------------------------
GITLAB = [fv("gitlabApiBase"), "/projects/", fv("gitlabProjectId")]
rest("gitcommit1", "Commit intents/<id>/intent.json", "POST", GITLAB + ["/repository/commits"],
     [("PRIVATE-TOKEN", [cv("frontDoorGitLabToken")]), ("Content-Type", ["application/json"])], 3220, 200,
     raw_body=[out("buildinten", "commitBody")])
link("gitcommit1", "evcommit01", 3340, 200, ["gitmrcreat"])
rest("gitmrcreat", "Open merge request", "POST", GITLAB + ["/merge_requests"],
     [("PRIVATE-TOKEN", [cv("frontDoorGitLabToken")]), ("Content-Type", ["application/json"])], 3460, 200,
     raw_body=[out("buildinten", "mrBody")])
link("gitmrcreat", "evmrcrt001", 3580, 200, ["resultchck"])
function("resultchck", "Check GitLab results", "result.js", [
    ("commitStatus", "number", out("gitcommit1", "rawResponse.statusCode", "statusCode")),
    ("mrStatus", "number", out("gitmrcreat", "rawResponse.statusCode", "statusCode")),
    ("mrBody", OBJ, out("gitmrcreat", "rawResponse.body", "body")),
], {"result": S, "error": S, "mrUrl": S}, 3700, 200)
link("resultchck", "evresult01", 3820, 200, ["resultokay"])
equals("resultokay", "Submitted?", out("resultchck", "result"), "ok", 3940, 200)
link("resultokay", "evdone0001", 4060, 200, ["donepage01", "gitfailed1"], false_targets=["gitfailed1"])
message("donepage01", "Merge request opened",
        ["Merge request opened: ", out("resultchck", "mrUrl"),
         " . A reviewer merges it; the protected pipeline then plans the promotion and waits for deployment approval."],
        4180, 200)
error("gitfailed1", "GitLab submission failed", ["Submission failed: ", out("resultchck", "error")], 4060, 360)


def variable(name, context, vtype, value, display, flow_scoped=True):
    v = {"context": context, "fields": {"type": vtype, "displayName": display, "value": value,
                                        "mutable": True, "min": 0, "max": 2000},
         "type": "property", "visibility": "private", "companyId": COMPANY}
    if context == "flow":
        v["flowId"] = FLOW_ID
        v["name"] = f"{name}##SK##flow##SK##{FLOW_ID}"
    else:
        v["name"] = f"{name}##SK##{context}"
    return v


# Environment-specific values come from a JSON file (second argument), so the
# generator and its example values file carry no tenant data.
VALUE_KEYS = ["adminEnvironmentId", "hostEnvironmentId", "sourceEnvironmentId", "targetEnvironmentId",
              "organizationId", "region", "davinciAdminRoleId", "gitlabApiBase", "gitlabProjectId",
              "gitlabDefaultBranch"]
VALUES = json.loads(pathlib.Path(sys.argv[2]).read_text()) if len(sys.argv) > 2 else {}
unknown = set(VALUES) - set(VALUE_KEYS)
if unknown:
    sys.exit(f"unknown keys in values file: {sorted(unknown)}")
TRIAL = {k: str(VALUES.get(k, "")) for k in VALUE_KEYS}
variables = [variable(k, "flow", "string", v, k) for k, v in TRIAL.items()]
variables += [variable("frontDoorWorkerBasicAuth", "company", "secret", "", "Front door worker Basic auth"),
              variable("frontDoorGitLabToken", "company", "secret", "", "Front door GitLab token")]

flow = {
    "companyId": COMPANY,
    "authTokenExpireIds": [],
    "connectorIds": sorted({v[0] for v in CONN.values()}),
    "createdDate": 1793232000000,
    "currentVersion": 1,
    "description": "Admins sign in with PingOne SSO; DaVinci Admins submit a promotion intent as a GitLab merge request. "
                   "Never changes PingOne: production changes run only in the protected GitLab pipeline.",
    "flowStatus": "enabled",
    "isOutputSchemaSaved": False,
    "name": "PingOne Promote Front Door",
    "publishedVersion": 1,
    "timeouts": "null",
    "updatedDate": 1793232000000,
    "flowId": FLOW_ID,
    "versionId": 1,
    "graphData": {
        "elements": {"nodes": nodes, "edges": edges},
        "data": {}, "zoomingEnabled": True, "userZoomingEnabled": True, "zoom": 1, "minZoom": 1e-50,
        "maxZoom": 1e50, "panningEnabled": True, "userPanningEnabled": True, "pan": {"x": 0, "y": 0},
        "boxSelectionEnabled": True, "renderer": {"name": "null"},
    },
    "flowColor": "#E3F0FF",
    "savedDate": 1793232000000,
    "variables": variables,
    "connections": [],
}
OUT.write_text(json.dumps(flow, indent=2) + "\n")
print(f"wrote {OUT}: {len([n for n in nodes if n['data']['nodeType'] == 'CONNECTION'])} steps, "
      f"{len([n for n in nodes if n['data']['nodeType'] == 'EVAL'])} evaluators, {len(edges)} edges")
