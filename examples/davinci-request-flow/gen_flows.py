"""Generate the PingOne Promote Request flow as a single-flow DaVinci export.

The flow does what the Go tool's local app does: an admin signs in, picks
resources from live snapshots, and the flow commits request.json to a
promotion/<id> branch and opens a merge request. Planning, approval, apply and
rollback stay in the GitLab pipeline (pingone-runner), unchanged.

usage: python3 gen_flows.py OUT_DIR [values.json]          DaVinci import JSON
       python3 gen_flows.py --terraform TF_DIR [--signin] flow.tf.json for terraform/

Sign-in (admin SSO + DaVinci Admin check) is off by default for the proof of
concept; "requireSignIn": true in values.json, or --signin, turns it on.

Each custom function node carries lib/core.js (scoped) plus one step body
from flows/fn/, exactly as test/load_fn.js wraps them for the Node tests.
"""
import json
import re
import pathlib
import sys

HERE = pathlib.Path(__file__).parent
# Embedded copy: full-line comments and indentation removed (never touches
# string or regex contents). test/load_fn.js applies the same transform.
CORE = "\n".join(line.strip() for line in (HERE / "lib" / "core.js").read_text().splitlines()
                 if line.strip() and not line.strip().startswith("//"))
FN_DIR = HERE / "flows" / "fn"
TERRAFORM = len(sys.argv) > 1 and sys.argv[1] == "--terraform"
if TERRAFORM:
    OUT = pathlib.Path(sys.argv[2])
    VALUES = {"requireSignIn": "--signin" in sys.argv[3:]}
else:
    OUT = pathlib.Path(sys.argv[1])
    VALUES = json.loads(pathlib.Path(sys.argv[2]).read_text()) if len(sys.argv) > 2 else {}

VALUE_KEYS = {
    "apiOrigin": "https://api.pingone.com",
    "authOrigin": "https://auth.pingone.com",
    "adminEnvironmentId": "",
    "hostEnvironmentId": "",
    # Up to four environments; the requester picks source and target from these.
    "env1Name": "", "env1Id": "", "env2Name": "", "env2Id": "",
    "env3Name": "", "env3Id": "", "env4Name": "", "env4Id": "",
    # Optional: the GitLab project whose pipeline deploys to each environment.
    # Empty = gitlabProjectId. Each project's PINGONE_TARGET_ENVIRONMENT_ID must be that environment.
    "env1GitlabEnvironment": "", "env2GitlabEnvironment": "", "env3GitlabEnvironment": "", "env4GitlabEnvironment": "",
    "organizationId": "",
    "davinciAdminRoleId": "",
    "sourceRegion": "",
    "targetRegion": "",
    "gitlabUrl": "",
    "gitlabProjectId": "",
    "gitlabEnvironment": "production",
}
# Optional: your DaVinci connection IDs, so the export is wired to them on import.
CONNECTION_KEYS = {"oidcConnectionId": "oidc", "httpConnectionId": "http", "functionsConnectionId": "fn",
                   "errorConnectionId": "err", "variablesConnectionId": "var"}
# Admin SSO + DaVinci Admin check. Off for the proof of concept; set
# "requireSignIn": true (or --terraform DIR --signin) to restore it.
SIGNIN = VALUES.get("requireSignIn", False) is True
unknown = set(VALUES) - set(VALUE_KEYS) - set(CONNECTION_KEYS) - {"requireSignIn"}
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
for key, slot in CONNECTION_KEYS.items():
    cid = str(VALUES.get(key, "")).strip()
    if cid:
        if not re.fullmatch(r"[0-9a-fA-F]{32}", cid):
            sys.exit(f"{key} must be the 32-character hex DaVinci connection ID, got {cid!r}")
        CONN[slot] = (CONN[slot][0], cid.lower(), CONN[slot][2])


def fn_code(name):
    body = (FN_DIR / f"{name}.js").read_text()
    return ("module.exports = a = async ({params}) => {\nconst core = (() => {\n" + CORE +
            "\nreturn core;\n})();\n// ---- step: " + name + " ----\n" + body + "\n};\n")


def plain(*parts):
    """Plain text with {{references}}, as DaVinci stores code-editor fields such as a REST call's raw body."""
    return "".join(p if isinstance(p, str) else "{{" + p[1] + "}}" for p in parts)


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
        self.rest_fail = {}
        self.n = 0
        self.x, self.y = 100, 200

    def node_title(self, nid):
        return next(n["data"]["properties"]["nodeTitle"]["value"] for n in self.nodes if n["data"]["id"] == nid)

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
        """source -> evaluator -> target (true/default) [and false_target].
        A REST call's false branch is its failure: an error naming the step, or (soft) a pass-through to target."""
        if source in self.rest_fail and false_target is None:
            info = self.rest_fail[source]
            if info is None:
                false_target = self.save(self.node_title(source) + ": call failed, continue",
                                         [("lastFailedCall", "string", self.node_title(source))])
                self.link(false_target, target)
            else:
                title, hint = info
                false_target = self.error(f"Failed: {title}", [
                    f"Step \"{title}\" failed (HTTP ", self.out(source, "rawResponse.statusCode"), "). " + hint +
                    " Flow analytics show the full response."])
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

    def rest(self, title, method, url, headers, raw=None, soft=True):
        """soft (default): a failed call (non-2xx) still continues to the next step, a function that
        turns the status into a specific message. soft=False, for calls whose next step does not
        check the status: a failed call ends on an error naming this step."""
        first = url[0]
        if "/as/token" in url:
            hint = ("Check hostEnvironmentId (the worker app's environment), authOrigin, and the worker client ID and secret "
                    "in promoteWorkerBasicAuth.")
        elif first == ("node", "local.BUILD.payload.output.gitlabApi", "gitlabApi"):
            hint = "Check the GitLab token (promoteGitlabToken: api scope, Developer role) and gitlabProjectId."
        else:
            hint = ("HTTP 403 usually means the worker app has no role on that environment (give it the same roles as the "
                    "pipeline worker); 404 means a wrong environment ID or an API not enabled for it.")
        props = {"url": {"value": slate(*url)}, "httpMethod": {"value": method},
                 "headers": {"value": [{"key": k, "value": slate(*v)} for k, v in headers]}}
        if raw is not None:
            # As DaVinci saves the Body dropdown and raw editor (confirmed from a live export).
            props["httpBody"] = {"value": "raw"}
            props["raw"] = {"value": plain(*raw)}
        nid = self.node("http", "makeRestApiCall", props, title)
        self.rest_fail[nid] = None if soft else (title, hint)
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
            self.vars.setdefault(name, (vtype, ""))
            items.append({"name": name, "value": slate(*value) if isinstance(value, list) else slate(value),
                          "key": round(0.1 + i / 100, 4), "label": f"{name} ({vtype} - flow)", "type": vtype})
        return self.node("var", "saveFlowValue", {"saveFlowVariables": {"value": items}}, title)

    def error(self, title, message):
        """A styled final screen naming what failed (instead of DaVinci's generic error page)."""
        return self.page(title, notice_html(title, plain(*message), "error"), [])

    def message(self, title, parts):
        return self.page(title, notice_html(title, plain(*parts), "message"), [])

    def page(self, title, html, fields):
        return self.node("http", "customHTMLTemplate", {
            "customHTML": {"value": html},
            "formFieldsList": {"value": [{"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
                                          "displayName": f, "hashedVisibility": False} for f in fields]},
            "validationRules": {"value": []}}, title)

    def wait_page(self, title, text, step=None):
        """A short "working" page that submits itself after 2 s (Continue is the fallback).
        Long work is split across requests this way, so none hits the browser's timeout."""
        html = (wait_html(title, text, step) +
                # Exactly one submit per rendered page: DaVinci may run an inline script more than
                # once, and a second submit of a step that already moved on ends on its generic error.
                # The button is marked when clicked; a manual click marks it too.
                "<!-- pp-auto-continue --><script>(function () { function go() { var b = document.querySelector("
                "'[data-skbuttonvalue=\"CONTINUE\"]'); if (!b || b.getAttribute('data-pp-sent')) { return; } "
                "b.setAttribute('data-pp-sent', '1'); b.click(); } "
                "document.addEventListener('click', function (e) { var t = e.target && e.target.closest && "
                "e.target.closest('[data-skbuttonvalue=\"CONTINUE\"]'); if (t && e.isTrusted) { t.setAttribute('data-pp-sent', '1'); } }, true); "
                "setTimeout(go, 2000); })();</script>")
        return self.page(title, html, ["buttonValue"])

    # --- composite blocks -----------------------------------------------------
    def bearer(self):
        return [("Authorization", ["Bearer ", self.out(self.token, "rawResponse.body.access_token")])]

    def worker_token(self):
        """Sign-in off (POC): the flow starts at the worker token."""
        self.token = self.rest("Promotion worker token", "POST", [self.fv("authOrigin"), "/", self.fv("hostEnvironmentId"), "/as/token"],
                               [("Authorization", ["Basic ", self.cv("promoteWorkerBasicAuth")]),
                                ("Content-Type", ["application/x-www-form-urlencoded"])], raw=["grant_type=client_credentials"], soft=False)
        return self.token

    def sign_in(self):
        """Admin SSO -> userinfo -> worker token -> role checks. Returns (first, last-allow-node)."""
        s = self.node("oidc", "loginFirstFactor", {}, "Sign in with PingOne (admin SSO)")
        ui = self.rest("Who signed in (userinfo)", "GET", [self.fv("authOrigin"), "/", self.fv("adminEnvironmentId"), "/as/userinfo"],
                       [("Authorization", ["Bearer ", self.out(s, "accessToken")])])
        self.link(s, ui)
        self.token = self.rest("Promotion worker token", "POST", [self.fv("authOrigin"), "/", self.fv("hostEnvironmentId"), "/as/token"],
                               [("Authorization", ["Basic ", self.cv("promoteWorkerBasicAuth")]),
                                ("Content-Type", ["application/x-www-form-urlencoded"])], raw=["grant_type=client_credentials"], soft=False)
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
            ("environmentIds", "string", [self.fv("env1Id"), ",", self.fv("env2Id"), ",", self.fv("env3Id"), ",", self.fv("env4Id")]),
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
        init = self.save(f"{label(prefix)}: start paging", init_vars)
        if after:
            self.link(after, init)
        get = self.rest(f"{label(prefix)}: read page", "GET", [self.fv(f"{prefix}Url")], self.bearer())
        self.link(init, get)
        col = self.fn(f"{label(prefix)}: collect page", "page_collect", [
            ("collectionKey", "string", key), ("pagesSoFar", "object", self.fv(f"{prefix}Pages")),
            ("seenUrls", "object", self.fv(f"{prefix}Seen")), ("currentUrl", "string", self.fv(f"{prefix}Url")),
            ("firstUrl", "string", self.fv(f"{prefix}First")), ("apiOrigin", "string", self.fv("apiOrigin")),
            ("statusCode", "number", self.out(get, "rawResponse.statusCode")), ("body", "object", self.out(get, "rawResponse.body")),
        ], {"state": "string", "error": "string", "pages": "array", "seenUrls": "array", "nextUrl": "string", "pause": "string"})
        self.link(get, col)
        sv = self.save(f"{label(prefix)}: keep page", [(f"{prefix}Pages", "object", [self.out(col, "pages")]),
                                               (f"{prefix}Seen", "object", [self.out(col, "seenUrls")]),
                                               (f"{prefix}Url", "string", [self.out(col, "nextUrl")])])
        self.link(col, sv)
        err = None
        if soft:
            err = self.save(f"{label(prefix)}: note unreadable", [(f"{prefix}Error", "string", [self.out(col, "error")]),
                                                         (f"{prefix}Pages", "object", "[]")])
            self.link(err, cont)
        # Every few pages, hand back to the browser so no request runs long.
        pause = self.equals(f"{label(prefix)}: hand back to the browser?", self.out(col, "pause"), "true")
        wp = self.wait_page(f"{label(prefix)}: reading", f"Reading the {label(prefix).lower()} from PingOne.", 3 if prefix.endswith("Defs") else 2)
        self.link(pause, wp, false_target=get)
        self.link(wp, get)
        self.three_way_from(sv, col, "more", cont, pause, label(prefix) + " paging", err)
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
        g = self.rest(f"{label(prefix)}: read environment", "GET", [self.fv("apiOrigin"), "/v1/environments/", env], self.bearer(),
                      soft=False)
        if after:
            self.link(after, g)
        return g

    def live_snapshot(self, prefix, after, env, cont):
        """Environment record + new snapshot polled to COMPLETE + all pages. Leaves <prefix>Env, <prefix>Snap, <prefix>Pages."""
        g = self.env_record(prefix, after, env)
        keep_env = self.save(f"{label(prefix)}: keep environment", [(f"{prefix}Env", "object", [self.out(g, "rawResponse.body")])])
        self.link(g, keep_env)
        post = self.rest(f"{label(prefix)}: take snapshot", "POST", [self.fv("apiOrigin"), "/v1/environments/", env, "/snapshots?expand=all"],
                         self.bearer() + [("Content-Type", ["application/json"])],
                         raw=['{"baseResourceURL":"', self.fv("apiOrigin"), "/v1/environments/", env, '"}'])
        self.link(keep_env, post)
        poll_inputs = lambda src: [("environmentId", "string", env), ("attempts", "number", self.fv(f"{prefix}Attempts")),
                                   ("body", "object", self.out(src, "rawResponse.body")), ("statusCode", "number", self.out(src, "rawResponse.statusCode")),
                                   ("apiOrigin", "string", self.fv("apiOrigin"))]
        outputs = {"state": "string", "error": "string", "snapshotId": "string", "attempts": "number", "versionUrl": "string"}
        init = self.save(f"{label(prefix)}: reset poll counter", [(f"{prefix}Attempts", "number", "0")])
        self.link(post, init)
        p1 = self.fn(f"{label(prefix)}: snapshot ready?", "snapshot_poll", poll_inputs(post), outputs)
        self.link(init, p1)
        k1 = self.save(f"{label(prefix)}: keep snapshot", [(f"{prefix}Attempts", "number", [self.out(p1, "attempts")]),
                                                    (f"{prefix}SnapUrl", "string", [self.out(p1, "versionUrl")]),
                                                    (f"{prefix}SnapId", "string", [self.out(p1, "snapshotId")]),
                                                    (f"{prefix}Snap", "object", [self.out(post, "rawResponse.body")])])
        self.link(p1, k1)
        get = self.rest(f"{label(prefix)}: snapshot status", "GET", [self.fv(f"{prefix}SnapUrl")], self.bearer())
        p2 = self.fn(f"{label(prefix)}: snapshot ready? (poll)", "snapshot_poll", poll_inputs(get), outputs)
        self.link(get, p2)
        k2 = self.save(f"{label(prefix)}: keep snapshot (poll)", [(f"{prefix}Attempts", "number", [self.out(p2, "attempts")]),
                                                           (f"{prefix}Snap", "object", [self.out(get, "rawResponse.body")])])
        self.link(p2, k2)
        paging = self.pages(prefix, None, [self.fv(f"{prefix}SnapUrl"), "/embeddedSnapshots"], "referencedResources", cont)
        # pages() linked init from `after`=None: wire both poll branches to the paging start.
        # Each status check that isn't COMPLETE waits on a self-continuing page,
        # which also spaces the polls out.
        wp = self.wait_page(f"{label(prefix)}: preparing", f"PingOne is taking a snapshot of the {label(prefix).lower()}.", 2)
        self.link(wp, get)
        self.three_way_from(k1, p1, "wait", paging, wp, label(prefix) + " snapshot")
        self.three_way_from(k2, p2, "wait", paging, wp, label(prefix) + " snapshot (poll)")
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
        signin_only = set() if SIGNIN else {"adminEnvironmentId", "organizationId", "davinciAdminRoleId"}
        settings = {k: ("string", v) for k, v in SETTINGS.items() if k not in signin_only}
        for name, (vtype, value) in sorted({**settings, **self.vars}.items()):
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
            "companyId": COMPANY, "authTokenExpireIds": [], "connectorIds": sorted({n["data"]["connectorId"] for n in self.nodes if n["data"]["nodeType"] == "CONNECTION"}),
            "createdDate": 1793232000000, "currentVersion": 1, "description": self.description, "flowStatus": "enabled",
            "isOutputSchemaSaved": False, "name": self.name, "publishedVersion": 1, "timeouts": "null",
            "updatedDate": 1793232000000, "flowId": self.flow_id, "versionId": 1,
            "graphData": {"elements": {"nodes": self.nodes, "edges": self.edges}, "data": {}, "zoomingEnabled": True,
                          "userZoomingEnabled": True, "zoom": 1, "minZoom": 1e-50, "maxZoom": 1e50, "panningEnabled": True,
                          "userPanningEnabled": True, "pan": {"x": 0, "y": 0}, "boxSelectionEnabled": True, "renderer": {"name": "null"}},
            "settings": FLOW_SETTINGS, "flowColor": "#E3F0FF", "savedDate": 1793232000000, "variables": variables, "connections": [],
        }


# DaVinci defaults as in a live export (inline scripts need unsafe-inline); logLevel 3 = debug.
FLOW_SETTINGS = {"csp": "worker-src 'self' blob:; script-src 'self' https://cdn.jsdelivr.net https://code.jquery.com https://devsdk.singularkey.com http://cdnjs.cloudflare.com 'unsafe-inline' 'unsafe-eval';", "intermediateLoadingScreenCSS": "", "intermediateLoadingScreenHTML": "", "customTimeoutErrorScreenMessage": "", "customTimeoutErrorScreenHTML": "", "customTimeoutErrorScreenCSS": "", "logLevel": 3}


STEP_LABEL = {"liveSrc": "Source environment", "liveTgt": "Target environment",
              "srcDefs": "Source promotion variables", "tgtDefs": "Target promotion variables"}


def label(prefix):
    return STEP_LABEL.get(prefix, prefix)


# ---------------- screens -----------------------------------------------------
# Every page carries its own small stylesheet (scoped to .pp) so it renders the
# same in any DaVinci application, with or without the host page's CSS.
STEPS = ["Source", "Target", "Resources", "Review", "Submitted"]
CSS = """<style>
.pp{--pp-bg:#f4f6f9;--pp-card:#fff;--pp-ink:#1b2430;--pp-muted:#5b6675;--pp-line:#dde2e9;--pp-accent:#1f5fbf;--pp-accent-ink:#fff;
--pp-soft:#eaf1fb;--pp-code:#0f1720;--pp-code-ink:#d7e1ec;--pp-bad:#b42318;--pp-bad-soft:#fdecea;--pp-ok:#1a7f4b;--pp-ok-soft:#e7f6ee;
font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--pp-ink);background:var(--pp-bg);min-height:100vh;padding:32px 16px;box-sizing:border-box}
.pp *{box-sizing:border-box}
.pp-card{max-width:920px;margin:0 auto;background:var(--pp-card);border:1px solid var(--pp-line);border-radius:12px;box-shadow:0 1px 2px rgba(16,24,40,.06);overflow:hidden}
.pp-top{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:14px 24px;border-bottom:1px solid var(--pp-line)}
.pp-brand{font-weight:650;letter-spacing:.2px}.pp-brand span{color:var(--pp-accent)}
.pp-tag{font-size:12px;font-weight:600;color:#8a5a00;background:#fff4dc;border:1px solid #f3dca6;border-radius:999px;padding:2px 10px}
.pp-steps{display:flex;gap:4px;padding:14px 24px 0;margin:0;list-style:none;flex-wrap:wrap}
.pp-steps li{flex:1 1 0;min-width:90px;font-size:12px;font-weight:600;color:var(--pp-muted);padding-top:8px;border-top:3px solid var(--pp-line)}
.pp-steps li.done{color:var(--pp-ink);border-color:#8fb0e3}.pp-steps li.now{color:var(--pp-accent);border-color:var(--pp-accent)}
.pp-body{padding:20px 24px 26px}
.pp h1{font-size:22px;line-height:1.3;margin:4px 0 6px}
.pp-lead{color:var(--pp-muted);margin:0 0 18px}
.pp-route{display:inline-flex;align-items:center;gap:8px;flex-wrap:wrap;margin:0 0 18px}
.pp-chip{display:inline-block;background:var(--pp-soft);color:var(--pp-accent);border-radius:999px;padding:3px 12px;font-weight:600;font-size:13px}
.pp h2{font-size:14px;font-weight:650;color:var(--pp-muted);margin:20px 0 8px}
.pp pre{background:var(--pp-code);color:var(--pp-code-ink);border-radius:8px;padding:12px 14px;margin:0;max-height:300px;overflow:auto;
font:12.5px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre}
.pp-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}@media (max-width:720px){.pp-grid{grid-template-columns:1fr}}
.pp label{display:block;font-weight:600;margin:16px 0 6px}
.pp textarea,.pp input[type=text]{width:100%;border:1px solid #c5cdd8;border-radius:8px;padding:9px 11px;font:14px/1.45 ui-monospace,Menlo,Consolas,monospace;color:var(--pp-ink);background:#fff}
.pp textarea:focus,.pp input:focus{outline:2px solid #9cc0f5;border-color:var(--pp-accent)}
.pp code{background:var(--pp-soft);border-radius:4px;padding:0 4px;font-size:.92em}
.pp-actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:22px}
.pp-btn{appearance:none;border:1px solid var(--pp-accent);background:var(--pp-accent);color:var(--pp-accent-ink);border-radius:8px;padding:10px 18px;font:600 14px system-ui,sans-serif;cursor:pointer}
.pp-btn:hover{filter:brightness(1.08)}.pp-btn.ghost{background:#fff;color:var(--pp-ink);border-color:#c5cdd8}
.pp-envs{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:12px}
.pp-env{appearance:none;text-align:left;background:#fff;border:1px solid var(--pp-line);border-radius:10px;padding:16px;cursor:pointer;font:inherit;color:inherit;
transition:border-color .15s,box-shadow .15s}
.pp-env:hover,.pp-env:focus{border-color:var(--pp-accent);box-shadow:0 0 0 3px #d6e5fb;outline:none}
.pp-env b{display:block;font-size:16px}.pp-env small{color:var(--pp-muted)}
.pp-note{border-radius:8px;padding:12px 14px;margin:0 0 16px;background:var(--pp-soft)}
.pp-note.bad{background:var(--pp-bad-soft);color:var(--pp-bad);border:1px solid #f5c2bc}
.pp-note.ok{background:var(--pp-ok-soft);color:var(--pp-ok);border:1px solid #bfe3cf}
.pp-wait{display:flex;align-items:center;gap:14px;margin:8px 0 4px}
.pp-spin{width:28px;height:28px;border:3px solid var(--pp-line);border-top-color:var(--pp-accent);border-radius:50%;animation:pp-spin .8s linear infinite;flex:none}
@keyframes pp-spin{to{transform:rotate(360deg)}}
.pp a{color:var(--pp-accent);font-weight:600;word-break:break-all}
</style>"""
POC_TAG = '<span class="pp-tag">Proof of concept</span>'


def shell(step, body, kind="page"):
    """Card with brand bar and step tracker; step is an index into STEPS, or None."""
    steps = ""
    if step is not None:
        steps = '<ol class="pp-steps">' + "".join(
            f'<li class="{"now" if i == step else "done" if i < step else ""}">{i + 1}. {name}</li>' for i, name in enumerate(STEPS)) + "</ol>"
    return (CSS + f'<div class="pp" data-pp-kind="{kind}"><div class="pp-card"><div class="pp-top"><div class="pp-brand">PingOne <span>Promote</span></div>'
            f'{POC_TAG}</div>{steps}<div class="pp-body">{body}</div></div></div>')


def button(form_id, value, label, ghost=False, extra=""):
    return (f'<button type="submit" class="pp-btn{" ghost" if ghost else ""}"{extra} data-id="button" data-skcomponent="skbutton" '
            f'data-skbuttontype="form-submit" data-skform="{form_id}" data-skbuttonvalue="{value}">{label}</button>')


ROUTE = ('<div class="pp-route"><span class="pp-chip">{{global.flow.variables.sourceEnvName}}</span>&rarr;'
         '<span class="pp-chip">{{global.flow.variables.targetEnvName}}</span></div>')


def page_html(title, intro, pre_blocks, form_id, fields_html, buttons, step=None, route=False, grid=False, note=""):
    pres = "".join(f'<div><h2>{h}</h2><pre>{v}</pre></div>' for h, v in pre_blocks)
    if grid and pres:
        pres = f'<div class="pp-grid">{pres}</div>'
    btns = "".join(button(form_id, val, label, ghost="primary" not in cls) for val, label, cls in buttons)
    form = f'<form id="{form_id}" data-id="{form_id}">{fields_html}' + (f'<div class="pp-actions">{btns}</div>' if btns else "") + "</form>"
    return shell(step, f'<h1>{title}</h1><p class="pp-lead">{intro}</p>' + (ROUTE if route else "") + note + pres + form)


def env_page(title, intro, hide_name, step):
    """One tile per configured environment (ENV1..ENV4). Empty slots, and the
    already-chosen source on the target page, are hidden in the browser and
    refused again by resolve_env."""
    tiles = "".join(button("envForm", f"ENV{i}", f'<b>{{{{global.flow.variables.env{i}Name}}}}</b><small>Environment {i}</small>',
                           extra=' data-pp-env="1"').replace('class="pp-btn"', 'class="pp-env"') for i in range(1, 5))
    script = ("<script>(function () { var hide = " + json.dumps(hide_name) + "; document.querySelectorAll('[data-pp-env]')"
              ".forEach(function (b) { var n = b.querySelector('b'); var t = n ? n.textContent.trim() : '';"
              " if (!t || t === hide) { b.style.display = 'none'; } }); })();</script>")
    return shell(step, f'<h1>{title}</h1><p class="pp-lead">{intro}</p><form id="envForm" data-id="envForm"><div class="pp-envs">{tiles}</div></form>') + script


def wait_html(title, text, step):
    return shell(step, f'<h1>{title}</h1><div class="pp-wait"><div class="pp-spin"></div><div>{text}<br>'
                       '<small class="pp-lead">This page continues by itself.</small></div></div>'
                       '<form id="waitForm" data-id="waitForm"><div class="pp-actions">'
                       + button("waitForm", "CONTINUE", "Continue", ghost=True) + "</div></form>")


def notice_html(title, text, kind):
    """A final screen: kind 'error' (something failed) or 'message' (e.g. cancelled)."""
    note_cls = "bad" if kind == "error" else ""
    return shell(None, f'<h1>{title}</h1><div class="pp-note {note_cls}">{text}</div>'
                       '<p class="pp-lead">Close this window, or launch the flow again to start over.</p>', kind=kind)


def textarea(name, label, rows=3):
    return (f'<label for="{name}">{label}</label>'
            f'<textarea id="{name}" name="{name}" rows="{rows}" data-id="{name}-input"></textarea>')


def text_input(name, label):
    return (f'<label for="{name}">{label}</label>'
            f'<input type="text" id="{name}" name="{name}" autocomplete="off" data-id="{name}-input" />')


SRC = Flow.fv("sourceEnvironmentId")
TGT = Flow.fv("targetEnvironmentId")
# One promotion project for every environment; the GitLab environment named in
# the merge request is the chosen target's (targetGitlabEnvironment).
GITLAB_CFG = [("gitlabUrl", "string", Flow.fv("gitlabUrl")), ("gitlabProjectId", "string", Flow.fv("gitlabProjectId")),
              ("gitlabEnvironment", "string", Flow.fv("targetGitlabEnvironment"))]


def gitlab_headers(post=False):
    h = [("PRIVATE-TOKEN", [Flow.cv("promoteGitlabToken")]), ("Accept", ["application/json"])]
    return h + ([("Content-Type", ["application/json"])] if post else [])


def build_request_flow(signin):
    f = Flow("PingOne Promote - Request", "f0d00a7e00000000000000000000aa01",
             "A DaVinci Admin selects resources from live snapshots; the flow builds request.json exactly as the Go app does and "
             "opens a GitLab merge request. Deployment and rollback happen only in the protected GitLab pipeline.")
    # First: check the admin-set flow variables, naming any placeholder or typo.
    settings_check = f.fn("Check settings", "check_settings",
                          [(k, "string", f.fv(k)) for k in ("apiOrigin", "authOrigin", "hostEnvironmentId", "gitlabUrl", "gitlabProjectId")] +
                          [(f"env{i}{k}", "string", f.fv(f"env{i}{k}")) for i in range(1, 5) for k in ("Name", "Id", "GitlabEnvironment")] + [("gitlabEnvironment", "string", f.fv("gitlabEnvironment"))],
                          {"state": "string", "error": "string"})
    settings_ok = f.equals("Settings ok?", f.out(settings_check, "state"), "ok")
    f.link(settings_check, settings_ok)
    if signin:
        start, allow, denied = f.sign_in()
        who = lambda field: ("node", f"local.AUTHZ.payload.output.{field}", field)
    else:
        start = f.worker_token()
        who = lambda field: ""
    api = ("node", "local.BUILD.payload.output.gitlabApi", "gitlabApi")
    # Built back-to-front so each block knows its continuation.
    done = f.page("Merge request opened", page_html(
        "Promotion request submitted",
        "Nothing has changed in PingOne yet. After the merge request is approved and merged, the GitLab pipeline plans the "
        "promotion; deployment needs its own approval there.",
        [], "doneForm", "", [], step=4, route=True,
        note='<div class="pp-note ok">Merge request opened: <a href="{{local.MR.payload.output.webUrlText}}" target="_blank" '
             'rel="noopener">{{local.MR.payload.output.webUrlText}}</a></div>'), [])
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
        ("username", "string", who("username")),
        ("variablesMd", "string", ("node", "local.VARS.payload.output.variablesMd", "variablesMd")),
        ("userId", "string", who("userId"))],
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
        "previewForm", "", [("SUBMIT", "Submit to GitLab", "btn-primary"), ("CANCEL", "Cancel", "btn-outline-secondary")],
        step=3, route=True), ["buttonValue"])
    f.link(preview, submit_go)
    build = f.fn("Build request.json", "build_request", GITLAB_CFG + [
        ("selectedRaw", "string", ("node", "local.SELECT.payload.output.selectedRaw", "selectedRaw")),
        ("mappingsRaw", "string", ("node", "local.SELECT.payload.output.mappingsRaw", "mappingsRaw")),
        ("description", "string", ("node", "local.SELECT.payload.output.description", "description")),
        ("source", "object", ("node", "local.INVLIVE.payload.output.source", "source")),
        ("target", "object", ("node", "local.INVLIVE.payload.output.target", "target")),
        ("sourceRegion", "string", Flow.fv("sourceRegion")), ("targetRegion", "string", Flow.fv("targetRegion")),
        ("userId", "string", who("userId"))],
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
        {"state": "string", "error": "string", "found": "array", "index": "number", "nextId": "string", "pause": "string"})
    f.link(decl_get, decl_col)
    decl_keep = f.save("Next declaration", [("declFound", "object", [f.out(decl_col, "found")]),
                                            ("declIdx", "number", [f.out(decl_col, "index")]),
                                            ("declId", "string", [f.out(decl_col, "nextId")])])
    f.link(decl_col, decl_keep)
    decl_pause = f.equals("declarations: hand back to the browser?", f.out(decl_col, "pause"), "true")
    decl_wait = f.wait_page("Checking promotion variables", "Reading promotion-variable declarations from PingOne.", 3)
    f.link(decl_pause, decl_wait, false_target=decl_get)
    f.link(decl_wait, decl_get)
    f.three_way_from(decl_keep, decl_col, "more", src_defs, decl_pause, "declarations")
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
        ("Signed in as <strong>{{local.AUTHZ.payload.output.usernameText}}</strong>. " if signin else
         "Sign-in is off for this proof of concept, so anyone who can launch the flow can open a request. ") +
        "Pick the source resources to promote. This opens a GitLab merge request; nothing changes in PingOne until the "
        "pipeline deploys it.",
        [("Source resources (type, ID, name)", "{{local.INVLIVE.payload.output.sourceListText}}"),
         ("Target resources (for mappings)", "{{local.INVLIVE.payload.output.targetListText}}")],
        "selectForm",
        textarea("selectedRaw", "Resource IDs to promote (one per line or comma-separated)", 4) +
        textarea("mappingsRaw", "Optional mappings, one <code>sourceId=targetId</code> per line", 2) +
        textarea("description", "Why is this change needed? (becomes the merge request title)", 3),
        [("BUILD", "Build request", "btn-primary")], step=2, route=True, grid=True),
        ["selectedRaw", "mappingsRaw", "description", "buttonValue"])
    f.link(select, build)
    inv_live = f.inventories("Live inventories", False, "liveSrc", SRC, "liveTgt", TGT)
    inv_live_ok = f.equals("Live inventories ok?", f.out(inv_live, "state"), "ok")
    f.link(inv_live, inv_live_ok)
    f.link(inv_live_ok, select, false_target=f.fail("Could not read the environments", inv_live))
    live_tgt = f.live_snapshot("liveTgt", None, TGT, inv_live)
    live_src = f.live_snapshot("liveSrc", None, SRC, live_tgt)
    # Choose source and target from the configured environments, then snapshot them.
    env_inputs = ([(f"env{i}{k}", "string", f.fv(f"env{i}{k}")) for i in range(1, 5) for k in ("Id", "Name", "GitlabEnvironment")] +
                  [("defaultGitlabEnvironment", "string", f.fv("gitlabEnvironment"))])
    tgt_res = f.fn("Target environment chosen", "resolve_env", [
        ("choice", "string", ("node", "local.TGTPAGE.payload.output.buttonValue", "buttonValue")),
        ("excludeId", "string", f.fv("sourceEnvironmentId"))] + env_inputs,
        {"state": "string", "error": "string", "environmentId": "string", "name": "string", "nameText": "string", "gitlabEnvironment": "string"})
    tgt_save = f.save("Keep target environment", [("targetEnvironmentId", "string", [f.out(tgt_res, "environmentId")]),
                                                  ("targetEnvName", "string", [f.out(tgt_res, "nameText")]),
                                                  ("targetGitlabEnvironment", "string", [f.out(tgt_res, "gitlabEnvironment")])])
    tgt_ok = f.equals("Target valid?", f.out(tgt_res, "state"), "ok")
    f.link(tgt_res, tgt_ok)
    f.link(tgt_ok, tgt_save, false_target=f.fail("Invalid target environment", tgt_res))
    f.link(tgt_save, live_src)
    tgt_page = f.page("Choose target environment", env_page(
        "Choose the target environment", "Promote from <strong>{{global.flow.variables.sourceEnvName}}</strong> to:",
        "{{global.flow.variables.sourceEnvName}}", 1), ["buttonValue"])
    f.link(tgt_page, tgt_res)
    src_res = f.fn("Source environment chosen", "resolve_env", [
        ("choice", "string", ("node", "local.SRCPAGE.payload.output.buttonValue", "buttonValue")), ("excludeId", "string", "")] + env_inputs,
        {"state": "string", "error": "string", "environmentId": "string", "name": "string", "nameText": "string", "gitlabEnvironment": "string"})
    src_save = f.save("Keep source environment", [("sourceEnvironmentId", "string", [f.out(src_res, "environmentId")]),
                                                  ("sourceEnvName", "string", [f.out(src_res, "nameText")])])
    src_ok = f.equals("Source valid?", f.out(src_res, "state"), "ok")
    f.link(src_res, src_ok)
    f.link(src_ok, src_save, false_target=f.fail("Invalid source environment", src_res))
    f.link(src_save, tgt_page)
    src_page = f.page("Choose source environment", env_page(
        "Choose the source environment", "Which environment has the configuration you want to promote?", "", 0), ["buttonValue"])
    f.link(src_page, src_res)
    f.link(settings_ok, start, false_target=f.fail("Settings need attention", settings_check))
    if signin:
        f.link(allow, src_page, false_target=denied)
    else:
        f.link(start, src_page)
    f.alias = {"VARS": vars_report, "VSTART": vstart, "MR": mr_ok_fn, "MRPOST": mr_post, "COMMIT": commit, "BRANCHPOST": branch_post, "BODIES": bodies, "PROJ": proj,
               "SRCPAGE": src_page, "TGTPAGE": tgt_page, "BRGET": br_get, "PROJGET": proj_get, "PREVIEW": preview, "BUILD": build, "SELECT": select, "INVLIVE": inv_live,
               **({"AUTHZ": f.authz} if signin else {})}
    return f


def resolve_aliases(flow):
    raw = json.dumps(flow.export())
    for alias, nid in flow.alias.items():
        raw = raw.replace(f"local.{alias}.", f"local.{nid}.")
    return json.loads(raw)


# ---------------- Terraform (pingone provider >= 1.23) ------------------------
# Settings come from Terraform variables/locals; connections from
# local.connection_ids; runtime-only flow variables get no managed value.
TF_SETTING = {
    "apiOrigin": "${local.api_origin}", "authOrigin": "${local.auth_origin}",
    "adminEnvironmentId": "${var.admin_environment_id}", "hostEnvironmentId": "${var.host_environment_id}",
    **{f"env{i}{k}": "${try(var.environments[%d].%s, \"\")}" % (i - 1, k.lower()) for i in range(1, 5) for k in ("Name", "Id")},
    "organizationId": "${var.organization_id}", "davinciAdminRoleId": "${var.davinci_admin_role_id}",
    "sourceRegion": "${local.region}", "targetRegion": "${local.region}",
    "gitlabUrl": "${var.gitlab_url}", "gitlabProjectId": "${var.gitlab_project_id}",
    **{f"env{i}GitlabEnvironment": "${try(var.environments[%d].gitlab_environment, \"\")}" % (i - 1) for i in range(1, 5)},
    "gitlabEnvironment": "${var.gitlab_environment}",
}
assert set(TF_SETTING) == set(VALUE_KEYS)


def tf_literal(s):
    """A string Terraform must not interpolate."""
    return s.replace("${", "$${").replace("%{", "%%{")


def terraform(flow_data):
    slot_of = {cid: slot for slot, (_, cid, _) in CONN.items()}
    nodes = {}
    for n in flow_data["graphData"]["elements"]["nodes"]:
        d = n["data"]
        data = {"node_type": d["nodeType"], "label": d["label"]}
        if d["nodeType"] == "CONNECTION":
            data.update({"connection_id": "${local.connection_ids.%s}" % slot_of[d["connectionId"]], "connector_id": d["connectorId"],
                         "name": d["name"], "status": d["status"], "capability_name": d["capabilityName"], "type": d["type"]})
        if d.get("properties"):
            data["properties"] = tf_literal(json.dumps(d["properties"], separators=(",", ":")))
        nodes[d["id"]] = {"data": data, "position": n["position"], "group": "nodes", "removed": False, "selected": False,
                          "selectable": True, "locked": False, "grabbable": True, "pannable": False}
    edges = {e["data"]["id"]: {"data": {"source": e["data"]["source"], "target": e["data"]["target"]}, "position": {"x": 0, "y": 0},
                               "group": "edges", "removed": False, "selected": False, "selectable": True, "locked": False,
                               "grabbable": True, "pannable": True}
             for e in flow_data["graphData"]["elements"]["edges"]}
    flow = {"environment_id": "${var.host_environment_id}", "name": flow_data["name"], "description": flow_data["description"],
            "color": flow_data["flowColor"],
            "settings": {"log_level": FLOW_SETTINGS["logLevel"], "csp": FLOW_SETTINGS["csp"]},
            "graph_data": {"elements": {"nodes": nodes, "edges": edges}, "zooming_enabled": True, "user_zooming_enabled": True, "zoom": 1,
                           "min_zoom": 1e-50, "max_zoom": 1e50, "panning_enabled": True, "user_panning_enabled": True,
                           "pan": {"x": 0, "y": 0}, "box_selection_enabled": True, "renderer": '{"name":"null"}'}}
    variables = {}
    for v in flow_data["variables"]:
        if v["context"] != "flow":
            continue
        name = v["fields"]["displayName"]
        res = {"environment_id": "${var.host_environment_id}", "context": "flow", "name": name, "display_name": name,
               "data_type": v["fields"]["type"], "mutable": True, "flow": {"id": "${pingone_davinci_flow.request.id}"}}
        if name in TF_SETTING:
            res["value"] = {"string": TF_SETTING[name]}
        variables["flow_" + re.sub(r"[^A-Za-z0-9_]", "_", name)] = res
    return {"locals": {"flow_has_signin": SIGNIN},
            "resource": {"pingone_davinci_flow": {"request": flow}, "pingone_davinci_variable": variables}}


OUT.mkdir(parents=True, exist_ok=True)
if TERRAFORM:
    data = resolve_aliases(build_request_flow(SIGNIN))
    (OUT / "flow.tf.json").write_text(json.dumps(terraform(data), indent=1) + "\n")
    print(f"flow.tf.json: {sum(1 for n in data['graphData']['elements']['nodes'])} nodes, "
          f"{sum(1 for v in data['variables'] if v['context'] == 'flow')} flow variables")
    sys.exit(0)
OUT.mkdir(parents=True, exist_ok=True)
for builder, fname in [(lambda: build_request_flow(SIGNIN), "pingone-promote-request.flow.json")]:
    flow = builder()
    data = resolve_aliases(flow)
    (OUT / fname).write_text(json.dumps(data, separators=(",", ":")) + "\n")
    steps = sum(1 for n in data["graphData"]["elements"]["nodes"] if n["data"]["nodeType"] == "CONNECTION")
    print(f"{fname}: {steps} steps, {len(data['graphData']['elements']['edges'])} edges")
