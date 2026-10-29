"""Generate the PingOne Promote Request flow as a single-flow DaVinci export.

The flow does what the Go tool's local app does: an admin signs in, picks
resources from live snapshots, and the flow commits request.json to a
promotion/<id> branch and opens a merge request. Planning, approval, apply and
rollback stay in the GitLab pipeline (pingone-runner), unchanged.

usage: python3 gen_flows.py OUT_DIR [values.json]

Each custom function node carries lib/core.js (scoped) plus one step body
from flows/fn/, exactly as test/load_fn.js wraps them for the Node tests.
"""
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).parent
# Embedded copy: full-line comments and indentation removed (never touches
# string or regex contents). test/load_fn.js applies the same transform.
CORE = "\n".join(line.strip() for line in (HERE / "lib" / "core.js").read_text().splitlines()
                 if line.strip() and not line.strip().startswith("//"))
FN_DIR = HERE / "flows" / "fn"
OUT = pathlib.Path(sys.argv[1])
VALUES = json.loads(pathlib.Path(sys.argv[2]).read_text()) if len(sys.argv) > 2 else {}

VALUE_KEYS = {
    "apiOrigin": "https://api.pingone.com",
    "authOrigin": "https://auth.pingone.com",
    "adminEnvironmentId": "",
    "hostEnvironmentId": "",
    "sourceEnvironmentId": "",
    "targetEnvironmentId": "",
    "organizationId": "",
    "davinciAdminRoleId": "",
    "sourceRegion": "",
    "targetRegion": "",
    "gitlabUrl": "",
    "gitlabProjectId": "",
    "gitlabEnvironment": "production",
}
unknown = set(VALUES) - set(VALUE_KEYS)
if unknown:
    sys.exit(f"unknown keys in values file: {sorted(unknown)}")
SETTINGS = {k: str(VALUES.get(k, v)) for k, v in VALUE_KEYS.items()}

COMPANY = "00000000-0000-0000-0000-000000000000"
CONN = {
    "oidc": ("genericConnector", "a1f0d00a000000000000000000000001", "PingOne Admin SSO"),
    "http": ("httpConnector", "a1f0d00a000000000000000000000002", "Http"),
    "fn": ("functionsConnector", "a1f0d00a000000000000000000000003", "Functions"),
    "err": ("errorConnector", "a1f0d00a000000000000000000000004", "Error Message"),
    "var": ("variablesConnector", "a1f0d00a000000000000000000000005", "Variables"),
}


def fn_code(name):
    body = (FN_DIR / f"{name}.js").read_text()
    return ("module.exports = a = async ({params}) => {\nconst core = (() => {\n" + CORE +
            "\nreturn core;\n})();\n// ---- step: " + name + " ----\n" + body + "\n};\n")


def slate(*parts):
    children = []
    for p in parts:
        if isinstance(p, str):
            children.append({"text": p})
        else:
            _, refpath, label = p
            src = "variable.svg" if refpath.startswith("global.") else "http.svg"
            children.extend([{"text": ""}, {"type": "link", "src": src, "url": label, "data": "{{" + refpath + "}}",
                                            "tooltip": "{{" + refpath + "}}", "children": [{"text": label}]}, {"text": ""}])
    return json.dumps([{"children": children}], indent=2)


class Flow:
    def __init__(self, name, flow_id, description):
        self.name, self.flow_id, self.description = name, flow_id, description
        self.nodes, self.edges, self.vars = [], [], {}
        self.n = 0
        self.x, self.y = 100, 200

    # --- references -------------------------------------------------------
    @staticmethod
    def fv(name):
        return ("flow", f"global.flow.variables.{name}", name)

    @staticmethod
    def cv(name):
        return ("company", f"global.company.variables.{name}", name)

    @staticmethod
    def out(node, path):
        return ("node", f"local.{node}.payload.output.{path}", path.split(".")[-1])

    def var(self, name, vtype, value=""):
        self.vars[name] = (vtype, value)
        return self.fv(name)

    # --- nodes --------------------------------------------------------------
    def _id(self, prefix):
        self.n += 1
        return f"{prefix}{self.n:09d}"

    def _place(self):
        self.x += 160
        if self.x > 3300:
            self.x, self.y = 260, self.y + 220
        return {"x": self.x, "y": self.y}

    def node(self, conn, capability, props, title, ntype="trigger"):
        nid = self._id("n")
        connector_id, connection_id, cname = CONN[conn]
        props = dict(props)
        props["nodeTitle"] = {"value": title}
        self.nodes.append({"data": {"id": nid, "nodeType": "CONNECTION", "connectionId": connection_id, "connectorId": connector_id,
                                    "name": cname, "label": cname, "status": "configured", "capabilityName": capability,
                                    "type": ntype, "properties": props},
                           "position": self._place(), "group": "nodes", "removed": False, "selected": False, "selectable": True,
                           "locked": False, "grabbable": True, "pannable": False, "classes": ""})
        return nid

    def link(self, source, target, false_target=None):
        """source -> evaluator -> target (true/default) [and false_target]."""
        eid = self._id("e")
        data = {"id": eid, "nodeType": "EVAL", "label": "Evaluator"}
        if false_target:
            data["properties"] = {false_target: {"value": "allTriggersFalse"}}
        self.nodes.append({"data": data, "position": self._place(), "group": "nodes", "removed": False, "selected": False,
                           "selectable": True, "locked": False, "grabbable": True, "pannable": False, "classes": ""})
        for a, b in [(source, eid), (eid, target)] + ([(eid, false_target)] if false_target else []):
            self.edges.append({"data": {"id": self._id("g"), "source": a, "target": b}, "position": {"x": 0, "y": 0},
                               "group": "edges", "removed": False, "selected": False, "selectable": True, "locked": False,
                               "grabbable": True, "pannable": True, "classes": ""})

    def rest(self, title, method, url, headers, raw=None):
        props = {"url": {"value": slate(*url)}, "httpMethod": {"value": method},
                 "headers": {"value": [{"key": k, "value": slate(*v)} for k, v in headers]}}
        if raw is not None:
            props["bodyType"] = {"value": "raw"}
            props["rawBody"] = {"value": slate(*raw)}
        return self.node("http", "makeRestApiCall", props, title)

    def fn(self, title, step, inputs, outputs):
        return self.node("fn", "customFunction", {
            "variableInputList": {"value": [{"name": n, "preferredDataType": t, "value": slate(r)} for n, t, r in inputs]},
            "code": {"value": fn_code(step)},
            "outputSchema": {"value": json.dumps({"type": "object", "properties": {k: {"type": v} for k, v in outputs.items()}}, indent=2)},
        }, title)

    def equals(self, title, left, text):
        return self.node("fn", "AEqualsB", {"leftValueA": {"value": slate(left)}, "rightValueB": {"value": slate(text)}}, title)

    def save(self, title, assignments):
        items = []
        for i, (name, vtype, value) in enumerate(assignments):
            self.vars.setdefault(name, (vtype, ""))
            items.append({"name": name, "value": slate(*value) if isinstance(value, list) else slate(value),
                          "key": round(0.1 + i / 100, 4), "label": f"{name} ({vtype} - flow)", "type": vtype})
        return self.node("var", "saveFlowValue", {"saveFlowVariables": {"value": items}}, title)

    def error(self, title, message):
        return self.node("err", "customErrorMessage", {"errorMessage": {"value": slate(*message)}}, title, ntype="action")

    def message(self, title, parts):
        return self.node("http", "customHtmlMessage", {"message": {"value": slate(*parts)}, "showContinueButton": {"value": False}},
                         title, ntype="action")

    def page(self, title, html, fields):
        return self.node("http", "customHTMLTemplate", {
            "customHTML": {"value": html},
            "formFieldsList": {"value": [{"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
                                          "displayName": f, "hashedVisibility": False} for f in fields]},
            "validationRules": {"value": []}}, title)

    # --- composite blocks -----------------------------------------------------
    def bearer(self):
        return [("Authorization", ["Bearer ", self.out(self.token, "rawResponse.body.access_token")])]

    def sign_in(self):
        """Admin SSO -> userinfo -> worker token -> role checks. Returns (first, last-allow-node)."""
        s = self.node("oidc", "loginFirstFactor", {}, "Sign in with PingOne (admin SSO)")
        ui = self.rest("Who signed in (userinfo)", "GET", [self.fv("authOrigin"), "/", self.fv("adminEnvironmentId"), "/as/userinfo"],
                       [("Authorization", ["Bearer ", self.out(s, "accessToken")])])
        self.link(s, ui)
        self.token = self.rest("Promotion worker token", "POST", [self.fv("authOrigin"), "/", self.fv("hostEnvironmentId"), "/as/token"],
                               [("Authorization", ["Basic ", self.cv("promoteWorkerBasicAuth")]),
                                ("Content-Type", ["application/x-www-form-urlencoded"])], raw=["grant_type=client_credentials"])
        self.link(ui, self.token)
        ra = self.rest("Signed-in admin's role assignments", "GET",
                       [self.fv("apiOrigin"), "/v1/environments/", self.fv("adminEnvironmentId"), "/users/",
                        self.out(ui, "rawResponse.body.sub"), "/roleAssignments"], self.bearer())
        self.link(self.token, ra)
        rl = self.rest("PingOne role list", "GET", [self.fv("apiOrigin"), "/v1/roles"], self.bearer())
        self.link(ra, rl)
        az = self.fn("Allow only DaVinci Admins", "authz", [
            ("userinfoStatus", "number", self.out(ui, "rawResponse.statusCode")),
            ("userinfoBody", "object", self.out(ui, "rawResponse.body")),
            ("roleAssignmentsStatus", "number", self.out(ra, "rawResponse.statusCode")),
            ("roleAssignmentsBody", "object", self.out(ra, "rawResponse.body")),
            ("rolesListStatus", "number", self.out(rl, "rawResponse.statusCode")),
            ("rolesListBody", "object", self.out(rl, "rawResponse.body")),
            ("davinciAdminRoleId", "string", self.fv("davinciAdminRoleId")),
            ("organizationId", "string", self.fv("organizationId")),
            ("sourceEnvironmentId", "string", self.fv("sourceEnvironmentId")),
        ], {"decision": "string", "reason": "string", "userId": "string", "username": "string", "usernameText": "string"})
        self.link(rl, az)
        allow = self.equals("Allowed?", self.out(az, "decision"), "allow")
        denied = self.error("Access denied", ["Access denied: ", self.out(az, "reason")])
        self.link(az, allow)
        self.authz = az
        return s, allow, denied

    def fail(self, title, src_fn):
        return self.error(title, [title + ": ", self.out(src_fn, "error")])

    def pages(self, prefix, after, first_url, key, cont, soft=False):
        """Collect every page of a collection into flow variable <prefix>Pages, then go to cont.
        soft=True: a failed read records <prefix>Error and still continues (advisory data)."""
        init_vars = [(f"{prefix}Pages", "object", "[]"), (f"{prefix}Seen", "object", "[]"),
                     (f"{prefix}Url", "string", first_url), (f"{prefix}First", "string", first_url)]
        if soft:
            init_vars.append((f"{prefix}Error", "string", ""))
        init = self.save(f"{prefix}: start paging", init_vars)
        if after:
            self.link(after, init)
        get = self.rest(f"{prefix}: read page", "GET", [self.fv(f"{prefix}Url")], self.bearer())
        self.link(init, get)
        col = self.fn(f"{prefix}: collect page", "page_collect", [
            ("collectionKey", "string", key), ("pagesSoFar", "object", self.fv(f"{prefix}Pages")),
            ("seenUrls", "object", self.fv(f"{prefix}Seen")), ("currentUrl", "string", self.fv(f"{prefix}Url")),
            ("firstUrl", "string", self.fv(f"{prefix}First")), ("apiOrigin", "string", self.fv("apiOrigin")),
            ("statusCode", "number", self.out(get, "rawResponse.statusCode")), ("body", "object", self.out(get, "rawResponse.body")),
        ], {"state": "string", "error": "string", "pages": "array", "seenUrls": "array", "nextUrl": "string"})
        self.link(get, col)
        sv = self.save(f"{prefix}: keep page", [(f"{prefix}Pages", "object", [self.out(col, "pages")]),
                                               (f"{prefix}Seen", "object", [self.out(col, "seenUrls")]),
                                               (f"{prefix}Url", "string", [self.out(col, "nextUrl")])])
        self.link(col, sv)
        err = None
        if soft:
            err = self.save(f"{prefix}: note unreadable", [(f"{prefix}Error", "string", [self.out(col, "error")]),
                                                         (f"{prefix}Pages", "object", "[]")])
            self.link(err, cont)
        self.three_way_from(sv, col, "more", cont, get, prefix + " paging", err)
        return init

    def three_way_from(self, after, fn_node, state_more, cont, more_target, title, err=None):
        is_more = self.equals(f"{title}: more?", self.out(fn_node, "state"), state_more)
        self.link(after, is_more)
        done_word = {"wait": "complete", "more": "done"}[state_more]
        is_done = self.equals(f"{title}: finished?", self.out(fn_node, "state"), done_word)
        self.link(is_more, more_target, false_target=is_done)
        if err is None:
            err = self.fail(title + " failed", fn_node)
        self.link(is_done, cont, false_target=err)

    def env_record(self, prefix, after, env):
        g = self.rest(f"{prefix}: environment", "GET", [self.fv("apiOrigin"), "/v1/environments/", env], self.bearer())
        if after:
            self.link(after, g)
        return g

    def live_snapshot(self, prefix, after, env, cont):
        """Environment record + new snapshot polled to COMPLETE + all pages. Leaves <prefix>Env, <prefix>Snap, <prefix>Pages."""
        g = self.env_record(prefix, after, env)
        keep_env = self.save(f"{prefix}: keep environment", [(f"{prefix}Env", "object", [self.out(g, "rawResponse.body")])])
        self.link(g, keep_env)
        post = self.rest(f"{prefix}: take snapshot", "POST", [self.fv("apiOrigin"), "/v1/environments/", env, "/snapshots?expand=all"],
                         self.bearer() + [("Content-Type", ["application/json"])],
                         raw=['{"baseResourceURL":"', self.fv("apiOrigin"), "/v1/environments/", env, '"}'])
        self.link(keep_env, post)
        poll_inputs = lambda src: [("environmentId", "string", env), ("attempts", "number", self.fv(f"{prefix}Attempts")),
                                   ("body", "object", self.out(src, "rawResponse.body")), ("statusCode", "number", self.out(src, "rawResponse.statusCode")),
                                   ("apiOrigin", "string", self.fv("apiOrigin"))]
        outputs = {"state": "string", "error": "string", "snapshotId": "string", "attempts": "number", "versionUrl": "string"}
        init = self.save(f"{prefix}: reset poll counter", [(f"{prefix}Attempts", "number", "0")])
        self.link(post, init)
        p1 = self.fn(f"{prefix}: snapshot ready?", "snapshot_poll", poll_inputs(post), outputs)
        self.link(init, p1)
        k1 = self.save(f"{prefix}: keep snapshot", [(f"{prefix}Attempts", "number", [self.out(p1, "attempts")]),
                                                    (f"{prefix}SnapUrl", "string", [self.out(p1, "versionUrl")]),
                                                    (f"{prefix}SnapId", "string", [self.out(p1, "snapshotId")]),
                                                    (f"{prefix}Snap", "object", [self.out(post, "rawResponse.body")])])
        self.link(p1, k1)
        get = self.rest(f"{prefix}: snapshot status", "GET", [self.fv(f"{prefix}SnapUrl")], self.bearer())
        p2 = self.fn(f"{prefix}: snapshot ready? (poll)", "snapshot_poll", poll_inputs(get), outputs)
        self.link(get, p2)
        k2 = self.save(f"{prefix}: keep snapshot (poll)", [(f"{prefix}Attempts", "number", [self.out(p2, "attempts")]),
                                                           (f"{prefix}Snap", "object", [self.out(get, "rawResponse.body")])])
        self.link(p2, k2)
        paging = self.pages(prefix, None, [self.fv(f"{prefix}SnapUrl"), "/embeddedSnapshots"], "referencedResources", cont)
        # pages() linked init from `after`=None: wire both poll branches to the paging start.
        self.three_way_from(k1, p1, "wait", paging, get, prefix + " snapshot")
        self.three_way_from(k2, p2, "wait", paging, get, prefix + " snapshot (poll)")
        return g

    def inventories(self, title, pinned, src_prefix, src_env, tgt_prefix, tgt_env):
        return self.fn(title, "inventories", [
            ("apiOrigin", "string", self.fv("apiOrigin")), ("pinned", "string", "true" if pinned else "false"),
            ("sourceEnvironmentId", "string", src_env), ("targetEnvironmentId", "string", tgt_env),
            ("sourceEnvBody", "object", self.fv(f"{src_prefix}Env")), ("sourceSnapshotBody", "object", self.fv(f"{src_prefix}Snap")),
            ("sourcePages", "object", self.fv(f"{src_prefix}Pages")),
            ("sourceSnapshotId", "string", self.out_snap(src_prefix)),
            ("targetEnvBody", "object", self.fv(f"{tgt_prefix}Env")), ("targetSnapshotBody", "object", self.fv(f"{tgt_prefix}Snap")),
            ("targetPages", "object", self.fv(f"{tgt_prefix}Pages")),
            ("targetSnapshotId", "string", self.out_snap(tgt_prefix)),
        ], {"state": "string", "error": "string", "source": "object", "target": "object", "sourceListText": "string", "targetListText": "string"})

    def out_snap(self, prefix):
        return self.fv(f"{prefix}SnapId")

    # --- export -------------------------------------------------------------
    def export(self):
        variables = []
        for name, (vtype, value) in sorted({**{k: ("string", v) for k, v in SETTINGS.items()}, **self.vars}.items()):
            variables.append({"context": "flow", "fields": {"type": vtype, "displayName": name, "value": value, "mutable": True,
                                                            "min": 0, "max": 2000},
                              "flowId": self.flow_id, "type": "property", "visibility": "private",
                              "name": f"{name}##SK##flow##SK##{self.flow_id}", "companyId": COMPANY})
        variables.append({"context": "company", "fields": {"type": "secret", "displayName": "PingOne Promote worker (Basic auth)",
                                                           "value": "", "mutable": True, "min": 0, "max": 2000},
                          "type": "property", "visibility": "private", "name": "promoteWorkerBasicAuth##SK##company", "companyId": COMPANY})
        variables.append({"context": "company", "fields": {"type": "secret", "displayName": "PingOne Promote GitLab token (api scope, Developer)",
                                                           "value": "", "mutable": True, "min": 0, "max": 2000},
                          "type": "property", "visibility": "private", "name": "promoteGitlabToken##SK##company", "companyId": COMPANY})
        return {
            "companyId": COMPANY, "authTokenExpireIds": [], "connectorIds": sorted({c[0] for c in CONN.values()}),
            "createdDate": 1793232000000, "currentVersion": 1, "description": self.description, "flowStatus": "enabled",
            "isOutputSchemaSaved": False, "name": self.name, "publishedVersion": 1, "timeouts": "null",
            "updatedDate": 1793232000000, "flowId": self.flow_id, "versionId": 1,
            "graphData": {"elements": {"nodes": self.nodes, "edges": self.edges}, "data": {}, "zoomingEnabled": True,
                          "userZoomingEnabled": True, "zoom": 1, "minZoom": 1e-50, "maxZoom": 1e50, "panningEnabled": True,
                          "userPanningEnabled": True, "pan": {"x": 0, "y": 0}, "boxSelectionEnabled": True, "renderer": {"name": "null"}},
            "flowColor": "#E3F0FF", "savedDate": 1793232000000, "variables": variables, "connections": [],
        }


def page_html(title, intro, pre_blocks, form_id, fields_html, buttons):
    pres = "".join(f'<h2 class="h6 mt-3">{h}</h2><pre class="border rounded p-2 small" style="max-height: 300px; overflow: auto">{v}</pre>'
                   for h, v in pre_blocks)
    btns = "".join(f'<button type="submit" class="btn {cls} me-2" data-id="button" data-skcomponent="skbutton" data-skbuttontype="form-submit" '
                   f'data-skform="{form_id}" data-skbuttonvalue="{val}">{label}</button>' for val, label, cls in buttons)
    return (f'<div class="container py-4" style="max-width: 900px"><h1 class="h3 mb-3">{title}</h1><p class="text-muted">{intro}</p>{pres}'
            f'<form id="{form_id}" data-id="{form_id}">{fields_html}{btns}</form></div>')


def textarea(name, label, rows=3):
    return (f'<div class="mb-3"><label class="form-label" for="{name}">{label}</label>'
            f'<textarea class="form-control" id="{name}" name="{name}" rows="{rows}" data-id="{name}-input"></textarea></div>')


def text_input(name, label):
    return (f'<div class="mb-3"><label class="form-label" for="{name}">{label}</label>'
            f'<input class="form-control" type="text" id="{name}" name="{name}" autocomplete="off" data-id="{name}-input" /></div>')


SRC = Flow.fv("sourceEnvironmentId")
TGT = Flow.fv("targetEnvironmentId")
GITLAB_CFG = [("gitlabUrl", "string", Flow.fv("gitlabUrl")), ("gitlabProjectId", "string", Flow.fv("gitlabProjectId")),
              ("gitlabEnvironment", "string", Flow.fv("gitlabEnvironment"))]


def gitlab_headers(post=False):
    h = [("PRIVATE-TOKEN", [Flow.cv("promoteGitlabToken")]), ("Accept", ["application/json"])]
    return h + ([("Content-Type", ["application/json"])] if post else [])


def build_request_flow():
    f = Flow("PingOne Promote - Request", "f0d00a7e00000000000000000000aa01",
             "A DaVinci Admin selects resources from live snapshots; the flow builds request.json exactly as the Go app does and "
             "opens a GitLab merge request. Deployment and rollback happen only in the protected GitLab pipeline.")
    start, allow, denied = f.sign_in()
    api = ("node", "local.BUILD.payload.output.gitlabApi", "gitlabApi")
    # Built back-to-front so each block knows its continuation.
    done = f.page("Merge request opened", page_html(
        "Promotion request submitted",
        "Merge request: <a href=\"{{local.MR.payload.output.webUrlText}}\" target=\"_blank\" rel=\"noopener\">"
        "{{local.MR.payload.output.webUrlText}}</a>. Nothing has changed in PingOne. After the merge request is approved and merged, "
        "the protected GitLab pipeline plans the promotion; deployment needs its own approval there.",
        [], "doneForm", "", []), [])
    mr_ok_fn = f.fn("Merge request opened?", "gitlab_mr", GITLAB_CFG + [
        ("statusCode", "number", ("node", "local.MRPOST.payload.output.rawResponse.statusCode", "statusCode")),
        ("body", "object", ("node", "local.MRPOST.payload.output.rawResponse.body", "body"))],
        {"state": "string", "error": "string", "webUrl": "string", "webUrlText": "string", "iid": "number"})
    mr_ok = f.equals("MR ok?", f.out(mr_ok_fn, "state"), "ok")
    f.link(mr_ok_fn, mr_ok)
    f.link(mr_ok, done, false_target=f.fail("Could not open the merge request", mr_ok_fn))
    mr_post = f.rest("GitLab: open merge request", "POST", [api, "/merge_requests"], gitlab_headers(True),
                     raw=[("node", "local.BODIES.payload.output.mrBody", "mrBody")])
    f.link(mr_post, mr_ok_fn)
    commit_ok_fn = f.fn("Commit created?", "gitlab_step", [
        ("what", "string", "commit promotion request"),
        ("statusCode", "number", ("node", "local.COMMIT.payload.output.rawResponse.statusCode", "statusCode"))],
        {"state": "string", "error": "string"})
    commit_ok = f.equals("Commit ok?", f.out(commit_ok_fn, "state"), "ok")
    f.link(commit_ok_fn, commit_ok)
    f.link(commit_ok, mr_post, false_target=f.fail("Could not commit the request", commit_ok_fn))
    commit = f.rest("GitLab: commit request.json", "POST", [api, "/repository/commits"], gitlab_headers(True),
                    raw=[("node", "local.BODIES.payload.output.commitBody", "commitBody")])
    f.link(commit, commit_ok_fn)
    branch_ok_fn = f.fn("Branch created?", "gitlab_step", [
        ("what", "string", "create promotion branch"),
        ("statusCode", "number", ("node", "local.BRANCHPOST.payload.output.rawResponse.statusCode", "statusCode"))],
        {"state": "string", "error": "string"})
    branch_ok = f.equals("Branch ok?", f.out(branch_ok_fn, "state"), "ok")
    f.link(branch_ok_fn, branch_ok)
    f.link(branch_ok, commit, false_target=f.fail("Could not create the promotion branch", branch_ok_fn))
    branch_post = f.rest("GitLab: create promotion branch", "POST", [api, "/repository/branches"], gitlab_headers(True),
                         raw=[("node", "local.BODIES.payload.output.branchBody", "branchBody")])
    f.link(branch_post, branch_ok_fn)
    bodies = f.fn("Default branch protected? Build GitLab payloads", "gitlab_branch", GITLAB_CFG + [
        ("defaultBranch", "string", ("node", "local.PROJ.payload.output.defaultBranch", "defaultBranch")),
        ("statusCode", "number", ("node", "local.BRGET.payload.output.rawResponse.statusCode", "statusCode")),
        ("body", "object", ("node", "local.BRGET.payload.output.rawResponse.body", "body")),
        ("requestJson", "string", ("node", "local.BUILD.payload.output.requestJson", "requestJson")),
        ("username", "string", ("node", "local.AUTHZ.payload.output.username", "username")),
        ("variablesMd", "string", ("node", "local.VARS.payload.output.variablesMd", "variablesMd")),
        ("userId", "string", ("node", "local.AUTHZ.payload.output.userId", "userId"))],
        {"state": "string", "error": "string", "branch": "string", "branchBody": "string", "commitBody": "string", "mrBody": "string"})
    bodies_ok = f.equals("Branch protected?", f.out(bodies, "state"), "ok")
    f.link(bodies, bodies_ok)
    f.link(bodies_ok, branch_post, false_target=f.fail("GitLab project check failed", bodies))
    br_get = f.rest("GitLab: read default branch", "GET",
                    [api, "/repository/branches/", ("node", "local.PROJ.payload.output.defaultBranchPath", "defaultBranchPath")], gitlab_headers())
    f.link(br_get, bodies)
    proj = f.fn("GitLab project private and as configured?", "gitlab_project", GITLAB_CFG + [
        ("statusCode", "number", ("node", "local.PROJGET.payload.output.rawResponse.statusCode", "statusCode")),
        ("body", "object", ("node", "local.PROJGET.payload.output.rawResponse.body", "body"))],
        {"state": "string", "error": "string", "defaultBranch": "string", "defaultBranchPath": "string"})
    proj_ok = f.equals("Project ok?", f.out(proj, "state"), "ok")
    f.link(proj, proj_ok)
    f.link(proj_ok, br_get, false_target=f.fail("GitLab project check failed", proj))
    proj_get = f.rest("GitLab: read project", "GET", [api], gitlab_headers())
    f.link(proj_get, proj)
    submit_go = f.equals("Submit pressed?", ("node", "local.PREVIEW.payload.output.buttonValue", "buttonValue"), "SUBMIT")
    cancelled = f.message("Cancelled", ["Cancelled. Nothing was sent to GitLab."])
    f.link(submit_go, proj_get, false_target=cancelled)
    preview = f.page("Review request", page_html(
        "Review the promotion request",
        "{{local.BUILD.payload.output.summaryText}} Secret values are redacted. Check the configuration below; reviewers see the same "
        "file in the merge request.",
        [("Promotion variables: {{local.VARS.payload.output.summaryText}}", "{{local.VARS.payload.output.reportText}}"),
         ("requests/{{local.BUILD.payload.output.requestId}}/request.json", "{{local.BUILD.payload.output.previewText}}")],
        "previewForm", "", [("SUBMIT", "Submit to GitLab", "btn-primary"), ("CANCEL", "Cancel", "btn-outline-secondary")]), ["buttonValue"])
    f.link(preview, submit_go)
    build = f.fn("Build request.json", "build_request", GITLAB_CFG + [
        ("selectedRaw", "string", ("node", "local.SELECT.payload.output.selectedRaw", "selectedRaw")),
        ("mappingsRaw", "string", ("node", "local.SELECT.payload.output.mappingsRaw", "mappingsRaw")),
        ("description", "string", ("node", "local.SELECT.payload.output.description", "description")),
        ("source", "object", ("node", "local.INVLIVE.payload.output.source", "source")),
        ("target", "object", ("node", "local.INVLIVE.payload.output.target", "target")),
        ("sourceRegion", "string", Flow.fv("sourceRegion")), ("targetRegion", "string", Flow.fv("targetRegion")),
        ("userId", "string", f.out(f.authz, "userId"))],
        {"state": "string", "error": "string", "requestId": "string", "requestJson": "string", "gitlabApi": "string",
         "previewText": "string", "summaryText": "string"})
    build_ok = f.equals("Request valid?", f.out(build, "state"), "ok")
    f.link(build, build_ok)
    # Advisory promotion-variable check between build and review.
    vars_report = f.fn("Promotion variables ready?", "vars_report", [
        ("requestJson", "string", ("node", "local.BUILD.payload.output.requestJson", "requestJson")),
        ("declarations", "object", Flow.fv("declFound")),
        ("sourceDefinitionPages", "object", Flow.fv("srcDefsPages")), ("sourceDefinitionError", "string", Flow.fv("srcDefsError")),
        ("targetDefinitionPages", "object", Flow.fv("tgtDefsPages")), ("targetDefinitionError", "string", Flow.fv("tgtDefsError"))],
        {"state": "string", "error": "string", "open": "number", "count": "number", "summaryText": "string", "reportText": "string",
         "variablesMd": "string"})
    f.link(vars_report, preview)
    tgt_defs = f.pages("tgtDefs", None, [f.fv("apiOrigin"), "/v1/environments/", TGT, "/promotionVariableDefinitions"],
                       "variableDefinitions", vars_report, soft=True)
    src_defs = f.pages("srcDefs", None, [f.fv("apiOrigin"), "/v1/environments/", SRC, "/promotionVariableDefinitions"],
                       "variableDefinitions", tgt_defs, soft=True)
    decl_get = f.rest("Read promotion-variable declaration", "GET",
                      [f.fv("apiOrigin"), "/v1/environments/", SRC, "/promotionVariableDeclarations/", f.fv("declId")], f.bearer())
    decl_col = f.fn("Keep declaration", "decl_collect", [
        ("ids", "object", ("node", "local.VSTART.payload.output.ids", "ids")), ("found", "object", f.fv("declFound")),
        ("index", "number", f.fv("declIdx")), ("statusCode", "number", f.out(decl_get, "rawResponse.statusCode")),
        ("body", "object", f.out(decl_get, "rawResponse.body"))],
        {"state": "string", "error": "string", "found": "array", "index": "number", "nextId": "string"})
    f.link(decl_get, decl_col)
    decl_keep = f.save("Next declaration", [("declFound", "object", [f.out(decl_col, "found")]),
                                            ("declIdx", "number", [f.out(decl_col, "index")]),
                                            ("declId", "string", [f.out(decl_col, "nextId")])])
    f.link(decl_col, decl_keep)
    f.three_way_from(decl_keep, decl_col, "more", src_defs, decl_get, "declarations")
    vstart = f.fn("Resources to check for promotion variables", "vars_start", [
        ("requestJson", "string", ("node", "local.BUILD.payload.output.requestJson", "requestJson"))],
        {"state": "string", "error": "string", "ids": "array", "firstId": "string"})
    decl_init = f.save("Start declarations", [("declFound", "object", "[]"), ("declIdx", "number", "0"),
                                              ("declId", "string", [f.out(vstart, "firstId")])])
    vstart_ok = f.equals("Selection readable?", f.out(vstart, "state"), "ok")
    f.link(vstart, vstart_ok)
    f.link(vstart_ok, decl_init, false_target=f.fail("Invalid request", vstart))
    f.link(decl_init, decl_get)
    f.link(build_ok, vstart, false_target=f.fail("Invalid request", build))
    select = f.page("Choose resources", page_html(
        "Request a PingOne promotion",
        "Signed in as <strong>{{local.AUTHZ.payload.output.usernameText}}</strong>. Source <code>{{global.flow.variables.sourceEnvironmentId}}</code> "
        "&rarr; target <code>{{global.flow.variables.targetEnvironmentId}}</code>. This opens a GitLab merge request; nothing changes in "
        "PingOne until the protected pipeline deploys it.",
        [("Source resources (type, ID, name)", "{{local.INVLIVE.payload.output.sourceListText}}"),
         ("Target resources (for mappings)", "{{local.INVLIVE.payload.output.targetListText}}")],
        "selectForm",
        textarea("selectedRaw", "Resource IDs to promote (one per line or comma-separated)", 4) +
        textarea("mappingsRaw", "Optional mappings, one <code>sourceId=targetId</code> per line", 2) +
        textarea("description", "Why is this change needed? (becomes the merge request title)", 3),
        [("BUILD", "Build request", "btn-primary")]), ["selectedRaw", "mappingsRaw", "description", "buttonValue"])
    f.link(select, build)
    inv_live = f.inventories("Live inventories", False, "liveSrc", SRC, "liveTgt", TGT)
    inv_live_ok = f.equals("Live inventories ok?", f.out(inv_live, "state"), "ok")
    f.link(inv_live, inv_live_ok)
    f.link(inv_live_ok, select, false_target=f.fail("Could not read the environments", inv_live))
    live_tgt = f.live_snapshot("liveTgt", None, TGT, inv_live)
    live_src = f.live_snapshot("liveSrc", None, SRC, live_tgt)
    f.link(allow, live_src, false_target=denied)
    f.alias = {"VARS": vars_report, "VSTART": vstart, "MR": mr_ok_fn, "MRPOST": mr_post, "COMMIT": commit, "BRANCHPOST": branch_post, "BODIES": bodies, "PROJ": proj,
               "BRGET": br_get, "PROJGET": proj_get, "PREVIEW": preview, "BUILD": build, "SELECT": select, "INVLIVE": inv_live,
               "AUTHZ": f.authz}
    return f


def resolve_aliases(flow):
    raw = json.dumps(flow.export())
    for alias, nid in flow.alias.items():
        raw = raw.replace(f"local.{alias}.", f"local.{nid}.")
    return json.loads(raw)


OUT.mkdir(parents=True, exist_ok=True)
for builder, fname in [(build_request_flow, "pingone-promote-request.flow.json")]:
    flow = builder()
    data = resolve_aliases(flow)
    (OUT / fname).write_text(json.dumps(data, separators=(",", ":")) + "\n")
    steps = sum(1 for n in data["graphData"]["elements"]["nodes"] if n["data"]["nodeType"] == "CONNECTION")
    print(f"{fname}: {steps} steps, {len(data['graphData']['elements']['edges'])} edges")
