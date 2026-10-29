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
                   "errorConnectionId": "err", "variablesConnectionId": "var", "flowConnectionId": "flow"}
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
    "flow": ("flowConnector", "a1f0d00a000000000000000000000006", "Flow Conductor"),
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
    def __init__(self, name, flow_id, description, letter="m", inputs=None):
        """inputs: for a subflow, its input schema [(name, type)], read as {{global.parameters.<name>}}."""
        self.name, self.flow_id, self.description, self.letter = name, flow_id, description, letter
        self.inputs = dict(inputs or [])
        self.claims = {}
        self.nodes, self.edges, self.vars = [], [], {}
        self.rest_fail, self.sub_fail = {}, {}
        self.token = None
        self.alias = {}
        self.context = []  # extra words for error screens, e.g. which environment a snapshot is for
        self.n = 0
        self.x, self.y = 100, 200

    def subject(self, prefix):
        """What a working page is about: the subflow's own wording if set, else e.g. "the source environment"."""
        return getattr(self, "what", None) or f"the {label(prefix).lower()}"

    def node_title(self, nid):
        return next(n["data"]["properties"]["nodeTitle"]["value"] for n in self.nodes if n["data"]["id"] == nid)

    # --- references -------------------------------------------------------
    @staticmethod
    def fv(name):
        return ("flow", f"global.flow.variables.{name}", name)

    @staticmethod
    def param(name):
        return ("param", f"global.parameters.{name}", name)

    def s(self, name):
        """A setting: the subflow's input parameter of that name, else the main flow's variable."""
        return self.param(name) if name in self.inputs else self.fv(name)

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
        # 10 characters, unique across the flows of one import file: flow letter + kind + counter.
        self.n += 1
        return f"{self.letter}{prefix}{self.n:08d}"

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
                false_target = self.error(f"Failed: {title}", [f"Step \"{title}\""] + self.context + [" failed (HTTP ",
                    self.out(source, "rawResponse.statusCode"), "). " + hint + " Flow analytics show the full response."])
        if source in self.sub_fail and false_target is None:
            false_target = self.error(f"Failed: {self.sub_fail[source]}", [
                f"The \"{self.sub_fail[source]}\" subflow did not finish. Open it in Flow analytics (log level is Debug) "
                "to see the step where it stopped."])
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
        elif isinstance(first, tuple) and first[2] == "gitlabApi":
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

    def conductor(self, title, sub, inputs):
        """Invoke UI Subflow (Flow Conductor), as in Ping's own flows: each input of the subflow's
        schema is a node property of the same name. A subflow that does not finish takes the false branch."""
        if set(inputs) != set(sub.inputs):
            raise SystemExit(f"{title}: inputs {sorted(set(inputs) ^ set(sub.inputs))} do not match the subflow's schema")
        props = {k: {"value": slate(*v) if isinstance(v, list) else slate(v)} for k, v in inputs.items()}
        props["subFlowId"] = {"value": {"label": sub.name, "value": sub.flow_id}}
        props["subFlowVersionId"] = {"value": -1}
        nid = self.node("flow", "startUiSubFlow", props, title)
        self.sub_fail[nid] = title
        return nid

    def success(self, title, claims):
        """Send Success JSON Response: the subflow's return values (claimsNameValuePairs, as Ping exports it)."""
        items = []
        for i, (name, vtype, value) in enumerate(claims):
            self.claims[name] = vtype
            items.append({"name": name, "value": slate(*value) if isinstance(value, list) else slate(value),
                          "key": round(0.2 + i / 100, 4), "label": f"{name} ({vtype} - flowInstance)", "type": vtype, "nameDefault": name})
        return self.node("http", "createSuccessResponse", {"claimsNameValuePairs": {"value": items}}, title, ntype="action")

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

    def page(self, title, html, fields, script=""):
        """A screen. Styles go in the node's CSS field and code in its Script field, as in Ping's
        own flows: DaVinci does not apply <style>/<script> written inside the HTML."""
        props = {
            "customHTML": {"value": html},
            "customCSS": {"value": CSS},
            "formFieldsList": {"value": [{"preferredControlType": "textField", "preferredDataType": "string", "propertyName": f,
                                          "displayName": f, "hashedVisibility": False} for f in fields]},
            "validationRules": {"value": []}}
        if script:
            props["customScript"] = {"value": script}
        return self.node("http", "customHTMLTemplate", props, title)

    def wait_page(self, title, text, step=None):
        """A short "working" page that submits itself after 2 s (Continue is the fallback).
        Long work is split across requests this way, so none hits the browser's timeout."""
        return self.page(title, wait_html(title, text, step), ["buttonValue"], script=AUTO_CONTINUE)

    # --- composite blocks -----------------------------------------------------
    def bearer(self):
        tok = self.out(self.token, "rawResponse.body.access_token") if self.token else self.param("accessToken")
        return [("Authorization", ["Bearer ", tok])]

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
            ("firstUrl", "string", self.fv(f"{prefix}First")), ("apiOrigin", "string", self.s("apiOrigin")),
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
        wp = self.wait_page(f"{label(prefix)}: reading", f"Reading {self.subject(prefix)} from PingOne.", 3 if prefix.endswith("Defs") else 2)
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
        g = self.rest(f"{label(prefix)}: read environment", "GET", [self.s("apiOrigin"), "/v1/environments/", env], self.bearer(),
                      soft=False)
        if after:
            self.link(after, g)
        return g

    def live_snapshot(self, prefix, after, env, cont):
        """Environment record + new snapshot polled to COMPLETE + all pages. Leaves <prefix>Env, <prefix>Snap, <prefix>Pages."""
        g = self.env_record(prefix, after, env)
        keep_env = self.save(f"{label(prefix)}: keep environment", [(f"{prefix}Env", "object", [self.out(g, "rawResponse.body")])])
        self.link(g, keep_env)
        post = self.rest(f"{label(prefix)}: take snapshot", "POST", [self.s("apiOrigin"), "/v1/environments/", env, "/snapshots?expand=all"],
                         self.bearer() + [("Content-Type", ["application/json"])],
                         raw=['{"baseResourceURL":"', self.s("apiOrigin"), "/v1/environments/", env, '"}'])
        self.link(keep_env, post)
        poll_inputs = lambda src: [("environmentId", "string", env), ("attempts", "number", self.fv(f"{prefix}Attempts")),
                                   ("body", "object", self.out(src, "rawResponse.body")), ("statusCode", "number", self.out(src, "rawResponse.statusCode")),
                                   ("apiOrigin", "string", self.s("apiOrigin"))]
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
        wp = self.wait_page(f"{label(prefix)}: preparing", f"PingOne is taking a snapshot of {self.subject(prefix)}.", 2)
        self.link(wp, get)
        self.three_way_from(k1, p1, "wait", paging, wp, label(prefix) + " snapshot")
        self.three_way_from(k2, p2, "wait", paging, wp, label(prefix) + " snapshot (poll)")
        return g

    def inventories(self, title, pinned, src_prefix, src_env, tgt_prefix, tgt_env):
        return self.fn(title, "inventories", [
            ("apiOrigin", "string", self.s("apiOrigin")), ("pinned", "string", "true" if pinned else "false"),
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
        # Admin settings live only in the main flow; subflows get what they need as inputs.
        settings = {k: ("string", v) for k, v in SETTINGS.items() if k not in signin_only} if not self.inputs and self.letter == "m" else {}
        used_company = set(re.findall(r"global\.company\.variables\.([A-Za-z0-9]+)", json.dumps(self.nodes)))
        for name, (vtype, value) in sorted({**settings, **self.vars}.items()):
            variables.append({"context": "flow", "fields": {"type": vtype, "displayName": name, "value": value, "mutable": True,
                                                            "min": 0, "max": 2000},
                              "flowId": self.flow_id, "type": "property", "visibility": "private",
                              "name": f"{name}##SK##flow##SK##{self.flow_id}", "companyId": COMPANY})
        for cname, display in (("promoteWorkerBasicAuth", "PingOne Promote worker (Basic auth)"),
                               ("promoteGitlabToken", "PingOne Promote GitLab token (api scope, Developer)")):
            if cname in used_company:
                variables.append({"context": "company", "fields": {"type": "secret", "displayName": display,
                                                                   "value": "", "mutable": True, "min": 0, "max": 2000},
                                  "type": "property", "visibility": "private", "name": f"{cname}##SK##company", "companyId": COMPANY})
        extra = {}
        if self.inputs:
            # As DaVinci exports a flow's Input Schema (Ping's own exports).
            extra["inputSchema"] = [{"description": "", "isExpanded": True, "preferredControlType": "textField",
                                     "preferredDataType": vt, "propertyName": name, "required": True} for name, vt in self.inputs.items()]
            extra["inputSchemaCompiled"] = {"parameters": {"type": "object", "properties": {
                name: {"description": "", "preferredDataType": vt, "isExpanded": True, "type": vt, "name": name} for name, vt in self.inputs.items()},
                "additionalProperties": False, "required": list(self.inputs)}}
            extra["isInputSchemaSaved"] = True
        if self.claims:
            extra["outputSchemaCompiled"] = {"output": {"type": "object", "additionalProperties": True,
                                                        "properties": {k: {"type": v} for k, v in self.claims.items()}}}
        return {**extra,
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
FLOW_SETTINGS = {"csp": "worker-src 'self' blob:; script-src 'self' https://cdn.jsdelivr.net https://code.jquery.com https://devsdk.singularkey.com http://cdnjs.cloudflare.com 'unsafe-inline' 'unsafe-eval';", "intermediateLoadingScreenCSS": "", "intermediateLoadingScreenHTML": "", "customTimeoutErrorScreenMessage": "", "customTimeoutErrorScreenHTML": "", "customTimeoutErrorScreenCSS": "", "logLevel": 3,
                 # Whole run (snapshots of two environments take minutes) and each backend call; defaults are 300 s and 15 s.
                 "flowTimeoutInSeconds": 3600, "flowHttpTimeoutInSeconds": 120}


STEP_LABEL = {"liveSrc": "Source environment", "liveTgt": "Target environment",
              "srcDefs": "Source promotion variables", "tgtDefs": "Target promotion variables"}


def label(prefix):
    return STEP_LABEL.get(prefix, prefix)


# ---------------- screens -----------------------------------------------------
# Every page carries its own small stylesheet (scoped to .pp) so it renders the
# same in any DaVinci application, with or without the host page's CSS.
STEPS = ["Source", "Target", "Resources", "Review", "Submitted"]
CSS = """
.pp{--pp-bg:#eef1f5;--pp-card:#fff;--pp-ink:#1a2433;--pp-muted:#5a6573;--pp-line:#d8dde4;--pp-bar:#0f2744;--pp-accent:#0b5cad;
--pp-accent-dark:#084a8c;--pp-soft:#e8f0fa;--pp-code:#f6f8fa;--pp-code-line:#e1e5ea;--pp-bad:#a4161a;--pp-bad-soft:#fdf1f1;
--pp-ok:#16683f;--pp-ok-soft:#eef8f2;
font:14px/1.55 "Segoe UI",system-ui,-apple-system,Roboto,"Helvetica Neue",Arial,sans-serif;color:var(--pp-ink);
background:var(--pp-bg);min-height:100vh;box-sizing:border-box;text-align:left}
.pp *{box-sizing:border-box}
.pp-bar{background:var(--pp-bar);color:#fff;display:flex;align-items:center;justify-content:space-between;gap:12px;padding:0 28px;height:56px}
.pp-brand{font-size:15px;font-weight:600;letter-spacing:.2px;display:flex;align-items:center;gap:10px}
.pp-mark{width:22px;height:22px;border-radius:4px;background:linear-gradient(135deg,#3d8bfd,#0b5cad);display:inline-block}
.pp-brand small{font-weight:400;color:#b7c4d6;font-size:13px;border-left:1px solid #3a5272;padding-left:10px}
.pp-tag{font-size:11px;font-weight:600;letter-spacing:.4px;text-transform:uppercase;color:#ffd58a;border:1px solid #8a6a2e;border-radius:3px;padding:2px 8px}
.pp-main{max-width:960px;margin:28px auto;padding:0 20px}
.pp-card{background:var(--pp-card);border:1px solid var(--pp-line);border-radius:6px;box-shadow:0 1px 3px rgba(15,39,68,.08)}
.pp-steps{display:flex;margin:0;padding:18px 28px;list-style:none;border-bottom:1px solid var(--pp-line);gap:0;flex-wrap:wrap}
.pp-steps li{flex:1 1 0;min-width:100px;display:flex;align-items:center;gap:8px;font-size:13px;color:var(--pp-muted);position:relative}
.pp-steps li span{width:24px;height:24px;border-radius:50%;border:2px solid #b9c2cd;display:inline-flex;align-items:center;justify-content:center;
font-size:12px;font-weight:600;background:#fff;flex:none}
.pp-steps li.done{color:var(--pp-ink)}.pp-steps li.done span{background:var(--pp-accent);border-color:var(--pp-accent);color:#fff}
.pp-steps li.now{color:var(--pp-accent);font-weight:600}.pp-steps li.now span{border-color:var(--pp-accent);color:var(--pp-accent)}
.pp-body{padding:24px 28px 28px}
.pp h1{font-size:20px;font-weight:600;line-height:1.3;margin:0 0 6px}
.pp-lead{color:var(--pp-muted);margin:0 0 18px}
.pp-route{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin:0 0 20px;padding:10px 14px;background:var(--pp-soft);border-radius:4px;font-size:13px}
.pp-route b{font-weight:600;color:var(--pp-muted);text-transform:uppercase;font-size:11px;letter-spacing:.5px}
.pp-chip{font-weight:600;color:var(--pp-accent-dark)}
.pp h2{font-size:13px;font-weight:600;color:var(--pp-ink);margin:22px 0 8px}
.pp pre{background:var(--pp-code);color:#24303f;border:1px solid var(--pp-code-line);border-radius:4px;padding:12px 14px;margin:0;max-height:300px;
overflow:auto;font:12.5px/1.6 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre}
.pp-grid{display:grid;grid-template-columns:1fr 1fr;gap:20px}@media (max-width:760px){.pp-grid{grid-template-columns:1fr}}
.pp label{display:block;font-weight:600;font-size:13px;margin:18px 0 6px}
.pp textarea,.pp input[type=text]{width:100%;border:1px solid #b9c2cd;border-radius:4px;padding:8px 10px;font:13px/1.5 ui-monospace,Menlo,Consolas,monospace;
color:var(--pp-ink);background:#fff}
.pp textarea:focus,.pp input:focus{outline:none;border-color:var(--pp-accent);box-shadow:0 0 0 3px rgba(11,92,173,.18)}
.pp code{background:var(--pp-soft);border-radius:3px;padding:0 4px;font-size:.92em}
.pp-actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:24px;padding-top:18px;border-top:1px solid var(--pp-line)}
.pp-btn{appearance:none;border:1px solid var(--pp-accent);background:var(--pp-accent);color:#fff;border-radius:4px;padding:9px 20px;
font:600 14px "Segoe UI",system-ui,sans-serif;cursor:pointer}
.pp-btn:hover{background:var(--pp-accent-dark)}.pp-btn.ghost{background:#fff;color:var(--pp-ink);border-color:#b9c2cd}.pp-btn.ghost:hover{background:#f3f5f8}
.pp-envs{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.pp-env{appearance:none;text-align:left;background:#fff;border:1px solid var(--pp-line);border-left:4px solid #b9c2cd;border-radius:4px;padding:14px 16px;
cursor:pointer;font:inherit;color:inherit}
.pp-env:hover,.pp-env:focus{border-color:var(--pp-accent);border-left-color:var(--pp-accent);background:#f8fbff;outline:none}
.pp-env b{display:block;font-size:15px;font-weight:600}.pp-env small{color:var(--pp-muted)}
.pp-env:has(b:empty),.pp-env[data-pp-id=""]{display:none}
.pp-note{border-radius:4px;padding:12px 14px;margin:0 0 16px;background:var(--pp-soft);border-left:4px solid var(--pp-accent)}
.pp-note.bad{background:var(--pp-bad-soft);color:var(--pp-bad);border-left-color:var(--pp-bad)}
.pp-note.ok{background:var(--pp-ok-soft);color:var(--pp-ok);border-left-color:var(--pp-ok)}
.pp-wait{display:flex;align-items:center;gap:14px;margin:6px 0 2px}
.pp-fallback{display:flex;align-items:center;gap:12px;margin-top:22px;color:var(--pp-muted);font-size:13px;visibility:hidden;animation:pp-reveal 0s linear 10s forwards}
@keyframes pp-reveal{to{visibility:visible}}
.pp-spin{width:26px;height:26px;border:3px solid var(--pp-line);border-top-color:var(--pp-accent);border-radius:50%;animation:pp-spin .8s linear infinite;flex:none}
@keyframes pp-spin{to{transform:rotate(360deg)}}
.pp a{color:var(--pp-accent);font-weight:600;word-break:break-all}
.pp-foot{margin:14px 2px 28px;color:var(--pp-muted);font-size:12px}
.pp-wide{max-width:1240px}
.pp [hidden]{display:none!important}
.pp-sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
.pp-hint{font-weight:400;color:var(--pp-muted);margin-left:6px}
.pp-toolbar{display:flex;flex-wrap:wrap;align-items:center;gap:12px;margin:0 0 12px}
.pp-toolbar label{margin:0}
.pp-search{flex:1 1 320px}
.pp-toolbar input[type=search],.pp-toolbar select,.pp select.pp-map{border:1px solid #b9c2cd;border-radius:4px;padding:8px 10px;font:13px "Segoe UI",system-ui,sans-serif;
color:var(--pp-ink);background:#fff}
.pp-toolbar input[type=search]{width:100%}
.pp-count{margin-left:auto;font-weight:600;color:var(--pp-accent-dark);background:var(--pp-soft);border-radius:999px;padding:4px 12px;font-size:12px}
.pp-tablewrap{border:1px solid var(--pp-line);border-radius:4px;overflow:auto;max-height:560px}
.pp-table{width:100%;border-collapse:collapse;font-size:13px}
.pp-table th{position:sticky;top:0;background:#f6f8fa;text-align:left;font-weight:600;color:var(--pp-muted);font-size:12px;
text-transform:uppercase;letter-spacing:.4px;padding:10px 12px;border-bottom:1px solid var(--pp-line);z-index:1}
.pp-table td{padding:10px 12px;border-bottom:1px solid #edf0f3;vertical-align:top}
.pp-table tr.pp-on td{background:#f3f8ff}
.pp-table tr:hover td{background:#f8fafc}
.pp-check{width:36px}
.pp-check input{width:16px;height:16px;margin-top:2px}
.pp-rname{display:block;font-weight:600}
.pp-mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px}
.pp-rid{display:block;color:var(--pp-muted)}
.pp-type{display:inline-block;background:#eef1f5;border-radius:3px;padding:2px 8px;font-size:12px;white-space:nowrap}
.pp-deps{min-width:170px}
.pp-deplist{margin:6px 0 0;padding-left:16px;color:var(--pp-muted)}
.pp-depactions{list-style:none;margin:6px 0 0 -16px}
.pp select.pp-map{width:100%;min-width:240px}
.pp-mapnote{margin:4px 0 0;color:var(--pp-muted);font-size:12px}
.pp-empty{text-align:center;color:var(--pp-muted);padding:28px 12px}
.pp-tablefoot{display:flex;justify-content:space-between;align-items:center;margin:8px 0 6px;color:var(--pp-muted);font-size:12px}
.pp-link{appearance:none;background:none;border:0;padding:0;color:var(--pp-accent);font:600 13px "Segoe UI",system-ui,sans-serif;cursor:pointer}
.pp-link:disabled{color:#9aa5b1;cursor:not-allowed}
.pp-btn:disabled{background:#9db8d6;border-color:#9db8d6;cursor:not-allowed}
"""
POC_TAG = '<span class="pp-tag">Proof of concept</span>'


def shell(step, body, kind="page", wide=False):
    """Product bar, step tracker and card; step is an index into STEPS, or None. Styles come from the node's CSS field."""
    steps = ""
    if step is not None:
        steps = '<ol class="pp-steps">' + "".join(
            f'<li class="{"now" if i == step else "done" if i < step else ""}"><span>{i + 1}</span>{name}</li>' for i, name in enumerate(STEPS)) + "</ol>"
    return (f'<div class="pp" data-pp-kind="{kind}"><div class="pp-bar"><div class="pp-brand"><i class="pp-mark"></i>PingOne Promote'
            f'<small>Configuration promotion request</small></div>{POC_TAG}</div><div class="pp-main{" pp-wide" if wide else ""}"><div class="pp-card">{steps}'
            f'<div class="pp-body">{body}</div></div><div class="pp-foot">Requests open a GitLab merge request. Deployment and '
            f'rollback happen only in the protected GitLab pipeline.</div></div></div>')


def button(form_id, value, label, ghost=False, extra=""):
    return (f'<button type="submit" class="pp-btn{" ghost" if ghost else ""}"{extra} data-id="button" data-skcomponent="skbutton" '
            f'data-skbuttontype="form-submit" data-skform="{form_id}" data-skbuttonvalue="{value}">{label}</button>')


def route_html(scope="global.flow.variables"):
    return (f'<div class="pp-route"><b>From</b><span class="pp-chip">{{{{{scope}.sourceEnvName}}}}</span>&rarr;'
            f'<b>To</b><span class="pp-chip">{{{{{scope}.targetEnvName}}}}</span></div>')


def page_html(title, intro, pre_blocks, form_id, fields_html, buttons, step=None, route=False, grid=False, note=""):
    """route: False, True (main flow's variables) or the reference scope holding sourceEnvName/targetEnvName."""
    pres = "".join(f'<div><h2>{h}</h2><pre>{v}</pre></div>' for h, v in pre_blocks)
    if grid and pres:
        pres = f'<div class="pp-grid">{pres}</div>'
    btns = "".join(button(form_id, val, label, ghost="primary" not in cls) for val, label, cls in buttons)
    form = f'<form id="{form_id}" data-id="{form_id}">{fields_html}' + (f'<div class="pp-actions">{btns}</div>' if btns else "") + "</form>"
    scope = route if isinstance(route, str) else "global.flow.variables"
    return shell(step, f'<h1>{title}</h1><p class="pp-lead">{intro}</p>' + (route_html(scope) if route else "") + note + pres + form)


def env_page(title, intro, hide_name, step, scope="global.flow.variables"):
    """One tile per configured environment (ENV1..ENV4). Empty slots, and the
    already-chosen source on the target page, are hidden in the browser and
    refused again by resolve_env."""
    # data-pp-id lets the CSS hide a slot without an environment ID, even if the Script field doesn't run.
    tiles = "".join(button("envForm", f"ENV{i}", f'<b>{{{{{scope}.env{i}Name}}}}</b><small>Environment {i}</small>',
                           extra=f' data-pp-env="1" data-pp-id="{{{{{scope}.env{i}Id}}}}"').replace('class="pp-btn"', 'class="pp-env"')
                    for i in range(1, 5))
    # The Script field (ENV_SCRIPT) hides empty slots and the source named in data-pp-hide.
    return shell(step, f'<h1>{title}</h1><p class="pp-lead">{intro}</p><form id="envForm" data-id="envForm" data-pp-hide="{hide_name}">'
                       f'<div class="pp-envs">{tiles}</div></form>')


# Script fields (plain JavaScript, as Ping's flows store them).
# Exactly one submit per rendered page: a second submit of a step that has
# already moved on ends on DaVinci's generic error. A manual click counts too.
AUTO_CONTINUE = """// pp-auto-continue: submit this working page once, after 2 seconds.
(function () {
  function button() { return document.querySelector('[data-skbuttonvalue="CONTINUE"]'); }
  document.addEventListener('click', function (e) {
    var t = e.target && e.target.closest && e.target.closest('[data-skbuttonvalue="CONTINUE"]');
    if (t && e.isTrusted) { t.setAttribute('data-pp-sent', '1'); }
  }, true);
  setTimeout(function () {
    var b = button();
    if (!b || b.getAttribute('data-pp-sent')) { return; }
    b.setAttribute('data-pp-sent', '1');
    b.click();
  }, 2000);
})();
"""
ENV_SCRIPT = r"""// Hide empty environment slots, and the chosen source on the target page.
(function () {
  var form = document.getElementById('envForm');
  var hide = form ? (form.getAttribute('data-pp-hide') || '').trim() : '';
  // Same rule as core.isUnset: blank, null, filler or an unresolved {{reference}}.
  function unset(v) { var s = String(v || '').trim(); return s === '' || /^(null|undefined|none|n\/a|na|-+|unused|empty)$/i.test(s) || /^\{\{.*\}\}$/.test(s); }
  document.querySelectorAll('[data-pp-env]').forEach(function (b) {
    var n = b.querySelector('b');
    var t = n ? n.textContent.trim() : '';
    var id = (b.getAttribute('data-pp-id') || '').trim();
    if (unset(t) || unset(id) || (hide && t === hide)) { b.style.display = 'none'; }
  });
})();
"""


def picker_html(signin):
    """The resource picker, as the local app's Step 2: every source resource with its type, dependencies and a
    destination mapping; search, type filter, selection count. The Script field (PICKER_SCRIPT) builds the table
    from the picker data and keeps the hidden selectedRaw/mappingsRaw fields in step. Without it, the plain
    lists and text boxes below still work."""
    who = ("Signed in as <strong>{{global.parameters.usernameText}}</strong>. " if signin else
           "Sign-in is off for this proof of concept, so anyone who can launch the flow can open a request. ")
    table = ('<div class="pp-toolbar" id="ppToolbar" hidden><label class="pp-search"><span class="pp-sr">Search resources by name, type, or ID</span>'
             '<input type="search" id="ppSearch" placeholder="Search name, type, or ID" autocomplete="off"></label>'
             '<label><span class="pp-sr">Filter by resource type</span><select id="ppType"><option value="">All types</option></select></label>'
             '<span class="pp-count" id="ppCount">0 selected</span></div>'
             '<div class="pp-note bad" id="ppWarn" hidden></div>'
             '<div class="pp-tablewrap" id="ppTableWrap" hidden><table class="pp-table"><thead><tr><th class="pp-check"><span class="pp-sr">Select</span></th>'
             '<th>Source resource</th><th>Type</th><th>Dependencies</th><th>Destination mapping</th></tr></thead><tbody id="ppRows"></tbody></table></div>'
             '<div class="pp-tablefoot" id="ppFoot" hidden><span id="ppTotal"></span><button type="button" class="pp-link" id="ppClear">Clear selection</button></div>'
             '<textarea id="ppData" hidden>{{global.parameters.pickerData}}</textarea>')
    manual = ('<div id="ppManual" class="pp-grid"><div><h2>Source resources (type, ID, name)</h2><pre>{{global.parameters.sourceListText}}</pre></div>'
              '<div><h2>Target resources (for mappings)</h2><pre>{{global.parameters.targetListText}}</pre></div></div>'
              '<div id="ppManualFields">' +
              textarea("selectedRaw", "Resource IDs to promote (one per line or comma-separated)", 4) +
              textarea("mappingsRaw", "Optional mappings, one <code>sourceId=targetId</code> per line", 2) + '</div>')
    body = (f'<h1>Choose resources</h1><p class="pp-lead">{who}Selecting a resource approves it for update. Dependencies stay unselected '
            'until you choose them. Nothing changes in PingOne until the GitLab pipeline deploys the request.</p>' + route_html("global.parameters") +
            table + '<form id="selectForm" data-id="selectForm">' + manual +
            textarea("description", "Why is this change needed? <span class=\"pp-hint\">Becomes the merge request title</span>", 2) +
            '<div class="pp-actions">' + button("selectForm", "BUILD", "Build request") + '</div></form>')
    return shell(2, body, wide=True)


PICKER_SCRIPT = r"""// pp-picker: the resource table of the local app's Step 2, filling the hidden selectedRaw and mappingsRaw fields.
(function () {
  var tries = 0;
  function start() {
    var dataEl = document.getElementById('ppData');
    if (!dataEl) { if (tries++ < 100) { setTimeout(start, 50); } return; }
    if (dataEl.getAttribute('data-pp-ready')) { return; }
    var data;
    try { data = JSON.parse(dataEl.value); } catch (e) { return; }
    dataEl.setAttribute('data-pp-ready', '1');
    var $ = function (id) { return document.getElementById(id); };
    var el = function (tag, cls, text) { var n = document.createElement(tag); if (cls) { n.className = cls; } if (text !== undefined) { n.textContent = text; } return n; };
    var source = Array.isArray(data.source) ? data.source : [];
    var target = Array.isArray(data.target) ? data.target : [];
    var byId = {}; source.forEach(function (r) { byId[String(r.id)] = r; });
    var selected = {}; var mappings = {}; var expanded = {};
    var suggestions = data.suggestions || {};
    var search = $('ppSearch'), type = $('ppType'), rows = $('ppRows'), selField = $('selectedRaw'), mapField = $('mappingsRaw');
    var build = document.querySelector('[data-skbuttonvalue="BUILD"]');
    function name(r) { return String(r.name || r.id); }
    function sync() {
      var ids = Object.keys(selected).filter(function (k) { return selected[k]; });
      selField.value = ids.join('\n');
      var lines = [];
      source.forEach(function (r) {
        var id = String(r.id);
        var m = Object.prototype.hasOwnProperty.call(mappings, id) ? mappings[id] : (suggestions[id] || '');
        if (m) { lines.push(id + '=' + m); }
      });
      mapField.value = lines.join('\n');
      $('ppCount').textContent = ids.length + ' selected';
      if (build) { build.disabled = ids.length === 0; }
    }
    function visible() {
      var q = search.value.trim().toLocaleLowerCase();
      return source.filter(function (r) {
        var hay = (name(r) + ' ' + r.type + ' ' + r.id).toLocaleLowerCase();
        return (!type.value || r.type === type.value) && (!q || hay.indexOf(q) >= 0);
      });
    }
    function depCell(r) {
      var td = el('td', 'pp-deps');
      var deps = Array.isArray(r.dependencies) ? r.dependencies : [];
      if (!deps.length) { td.textContent = 'None listed'; return td; }
      var id = String(r.id);
      var toggle = el('button', 'pp-link', expanded[id] ? 'Hide dependencies' : 'Show ' + deps.length + (deps.length === 1 ? ' dependency' : ' dependencies'));
      toggle.type = 'button';
      toggle.addEventListener('click', function () { expanded[id] = !expanded[id]; render(); });
      td.appendChild(toggle);
      if (expanded[id]) {
        var list = el('ul', 'pp-deplist');
        var unresolved = false;
        deps.forEach(function (d) {
          var item = byId[String(d)];
          if (!item) { unresolved = true; }
          var li = el('li');
          li.appendChild(el('span', '', item ? name(item) : String(d)));
          li.appendChild(document.createTextNode(' · '));
          li.appendChild(el('span', 'pp-mono', String(d)));
          list.appendChild(li);
        });
        var pick = el('button', 'pp-link', 'Select listed dependencies');
        pick.type = 'button';
        pick.disabled = unresolved;
        if (unresolved) { pick.title = 'Some dependencies are not present in the loaded inventory.'; }
        pick.addEventListener('click', function () { deps.forEach(function (d) { if (byId[String(d)]) { selected[String(d)] = true; } }); render(); });
        var actions = el('li', 'pp-depactions'); actions.appendChild(pick); list.appendChild(actions);
        td.appendChild(list);
      }
      return td;
    }
    function mapCell(r) {
      var id = String(r.id);
      var td = el('td');
      var opts = target.filter(function (t) { return t.type === r.type; });
      var sel = el('select', 'pp-map');
      sel.setAttribute('aria-label', 'Destination mapping for ' + name(r));
      var auto = el('option', '', 'Automatic / create if unmatched'); auto.value = ''; sel.appendChild(auto);
      opts.forEach(function (t) { var o = el('option', '', name(t) + ' · ' + t.id); o.value = String(t.id); sel.appendChild(o); });
      var mapped = Object.prototype.hasOwnProperty.call(mappings, id) ? mappings[id] : (suggestions[id] || '');
      sel.value = opts.some(function (t) { return String(t.id) === String(mapped); }) ? String(mapped) : '';
      sel.addEventListener('change', function () { mappings[id] = sel.value; sync(); });
      td.appendChild(sel);
      td.appendChild(el('p', 'pp-mapnote', opts.length ? opts.length + ' compatible destination ' + (opts.length === 1 ? 'resource' : 'resources') +
        ' · unselected resources can still be reused.' : 'No loaded destination match of this type. Automatic mapping may create a new resource.'));
      return td;
    }
    function render() {
      while (rows.firstChild) { rows.removeChild(rows.firstChild); }
      var shown = visible();
      shown.forEach(function (r) {
        var id = String(r.id);
        var tr = el('tr', selected[id] ? 'pp-on' : '');
        var c = el('td', 'pp-check'); var box = el('input'); box.type = 'checkbox'; box.checked = !!selected[id];
        box.setAttribute('aria-label', 'Select ' + name(r) + ' for promotion');
        box.addEventListener('change', function () { selected[id] = box.checked; render(); });
        c.appendChild(box);
        var s = el('td'); s.appendChild(el('span', 'pp-rname', name(r))); s.appendChild(el('span', 'pp-mono pp-rid', id));
        var ty = el('td'); ty.appendChild(el('span', 'pp-type', r.type));
        tr.appendChild(c); tr.appendChild(s); tr.appendChild(ty); tr.appendChild(depCell(r)); tr.appendChild(mapCell(r));
        rows.appendChild(tr);
      });
      if (!shown.length) {
        var tr = el('tr'); var td = el('td', 'pp-empty', source.length ? 'No matching resources. Change the search or type filter.' :
          'No supported configuration resources were found in the source environment.');
        td.colSpan = 5; tr.appendChild(td); rows.appendChild(tr);
      }
      $('ppTotal').textContent = shown.length + ' of ' + source.length + ' resources';
      sync();
    }
    var types = []; source.forEach(function (r) { if (types.indexOf(r.type) < 0) { types.push(r.type); } });
    types.sort(function (a, b) { return a.localeCompare(b); }).forEach(function (t) { var o = el('option', '', t); o.value = t; type.appendChild(o); });
    var warnings = Array.isArray(data.warnings) ? data.warnings : [];
    if (warnings.length) {
      var box = $('ppWarn'); box.hidden = false; box.appendChild(el('strong', '', 'Mapping suggestions need attention'));
      var ul = el('ul'); warnings.forEach(function (w) { ul.appendChild(el('li', '', String(w))); }); box.appendChild(ul);
    }
    search.addEventListener('input', render);
    type.addEventListener('change', render);
    $('ppClear').addEventListener('click', function () { selected = {}; render(); });
    $('ppToolbar').hidden = false; $('ppTableWrap').hidden = false; $('ppFoot').hidden = false;
    $('ppManual').hidden = true; $('ppManualFields').hidden = true;
    render();
  }
  start();
})();
"""


def wait_html(title, text, step):
    """A working page: spinner only. It submits itself (Script field); the Continue button is a fallback that
    the CSS reveals only if the page is still here after 10 seconds."""
    return shell(step, f'<h1>{title}</h1><div class="pp-wait"><div class="pp-spin"></div><div>{text}<br>'
                       '<small class="pp-lead">This page continues by itself.</small></div></div>'
                       '<form id="waitForm" data-id="waitForm"><div class="pp-fallback"><span>Still on this page?</span>'
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


# One promotion project for every environment; the GitLab environment named in
# the merge request is the chosen target's. Subflows receive these as inputs.
def gitlab_cfg(f):
    return [("gitlabUrl", "string", f.s("gitlabUrl")), ("gitlabProjectId", "string", f.s("gitlabProjectId")),
            ("gitlabEnvironment", "string", f.s("gitlabEnvironment"))]


def gitlab_headers(post=False):
    h = [("PRIVATE-TOKEN", [Flow.cv("promoteGitlabToken")]), ("Accept", ["application/json"])]
    return h + ([("Content-Type", ["application/json"])] if post else [])


P = Flow.param
FLOW_IDS = {"main": "f0d00a7e00000000000000000000aa01", "envs": "f0d00a7e00000000000000000000aa02",
            "snap": "f0d00a7e00000000000000000000aa03", "request": "f0d00a7e00000000000000000000aa04",
            "submit": "f0d00a7e00000000000000000000aa05"}
ENV_SLOT_INPUTS = [(f"env{i}{k}", "string") for i in range(1, 5) for k in ("Name", "Id", "GitlabEnvironment")] + [("gitlabEnvironment", "string")]


def build_envs_flow():
    """Subflow 1: the requester picks source and target from the configured slots."""
    f = Flow("PingOne Promote - 1 Choose environments", FLOW_IDS["envs"],
             "Subflow of PingOne Promote - Request: shows the environment slots and returns the chosen source and target.",
             letter="a", inputs=ENV_SLOT_INPUTS)
    env_inputs = ([(f"env{i}{k}", "string", P(f"env{i}{k}")) for i in range(1, 5) for k in ("Id", "Name", "GitlabEnvironment")] +
                  [("defaultGitlabEnvironment", "string", P("gitlabEnvironment"))])
    outputs = {"state": "string", "error": "string", "environmentId": "string", "name": "string", "nameText": "string",
               "gitlabEnvironment": "string"}
    src_page = f.page("Choose source environment", env_page(
        "Choose the source environment", "Which environment has the configuration you want to promote?", "", 0,
        scope="global.parameters"), ["buttonValue"], script=ENV_SCRIPT)
    src_res = f.fn("Source environment chosen", "resolve_env", [
        ("choice", "string", f.out(src_page, "buttonValue")), ("excludeId", "string", "")] + env_inputs, outputs)
    f.link(src_page, src_res)
    src_ok = f.equals("Source valid?", f.out(src_res, "state"), "ok")
    f.link(src_res, src_ok)
    src_save = f.save("Keep source environment", [("sourceEnvironmentId", "string", [f.out(src_res, "environmentId")]),
                                                  ("sourceEnvName", "string", [f.out(src_res, "nameText")])])
    f.link(src_ok, src_save, false_target=f.fail("Invalid source environment", src_res))
    tgt_page = f.page("Choose target environment", env_page(
        "Choose the target environment", "Promote from <strong>{{global.flow.variables.sourceEnvName}}</strong> to:",
        "{{global.flow.variables.sourceEnvName}}", 1, scope="global.parameters"), ["buttonValue"], script=ENV_SCRIPT)
    f.link(src_save, tgt_page)
    tgt_res = f.fn("Target environment chosen", "resolve_env", [
        ("choice", "string", f.out(tgt_page, "buttonValue")), ("excludeId", "string", f.fv("sourceEnvironmentId"))] + env_inputs, outputs)
    f.link(tgt_page, tgt_res)
    tgt_ok = f.equals("Target valid?", f.out(tgt_res, "state"), "ok")
    f.link(tgt_res, tgt_ok)
    done = f.success("Return chosen environments", [
        ("sourceEnvironmentId", "string", f.fv("sourceEnvironmentId")), ("sourceEnvName", "string", f.fv("sourceEnvName")),
        ("targetEnvironmentId", "string", f.out(tgt_res, "environmentId")), ("targetEnvName", "string", f.out(tgt_res, "nameText")),
        ("targetGitlabEnvironment", "string", f.out(tgt_res, "gitlabEnvironment"))])
    f.link(tgt_ok, done, false_target=f.fail("Invalid target environment", tgt_res))
    return f


def build_snapshot_flow():
    """Subflow 2 (used for the source, then the target): a fresh snapshot, polled to COMPLETE, every page read."""
    f = Flow("PingOne Promote - 2 Snapshot environment", FLOW_IDS["snap"],
             "Subflow of PingOne Promote - Request: takes a live snapshot of one environment and returns it with every page.",
             letter="b", inputs=[("apiOrigin", "string"), ("accessToken", "string"), ("environmentId", "string"),
                                 ("environmentLabel", "string")])
    f.what = "the {{global.parameters.environmentLabel}} environment"
    f.context = [" (", P("environmentLabel"), " environment)"]
    STEP_LABEL["snap"] = "Snapshot"
    # Every path starts on a screen, as an Invoke UI Subflow needs.
    intro = f.wait_page("Snapshot: starting", "Taking a snapshot of the <strong>{{global.parameters.environmentLabel}}</strong> environment.", 2)
    done = f.success("Return snapshot", [
        ("envBody", "object", f.fv("snapEnv")), ("snapshotBody", "object", f.fv("snapSnap")),
        ("snapshotId", "string", f.fv("snapSnapId")), ("pages", "array", f.fv("snapPages"))])
    first = f.live_snapshot("snap", None, P("environmentId"), done)
    f.link(intro, first)
    return f


REQ_INPUTS = [("apiOrigin", "string"), ("accessToken", "string"),
              ("sourceEnvironmentId", "string"), ("targetEnvironmentId", "string"),
              ("sourceEnvName", "string"), ("targetEnvName", "string"),
              ("sourceInventory", "object"), ("targetInventory", "object"),
              ("sourceListText", "string"), ("targetListText", "string"), ("pickerData", "string"),
              ("sourceRegion", "string"), ("targetRegion", "string"),
              ("gitlabUrl", "string"), ("gitlabProjectId", "string"), ("gitlabEnvironment", "string"),
              ("userId", "string"), ("usernameText", "string")]


def build_request_subflow(signin):
    """Subflow 3: choose resources, build request.json, check promotion variables, review. Returns on Submit."""
    f = Flow("PingOne Promote - 3 Choose resources and review", FLOW_IDS["request"],
             "Subflow of PingOne Promote - Request: the requester picks resources; the flow builds request.json exactly as the "
             "Go app does, checks promotion variables and shows the review. Returns only when the requester submits.",
             letter="c", inputs=REQ_INPUTS)
    src, tgt = P("sourceEnvironmentId"), P("targetEnvironmentId")
    select = f.page("Choose resources", picker_html(signin), ["selectedRaw", "mappingsRaw", "description", "buttonValue"],
                    script=PICKER_SCRIPT)
    build = f.fn("Build request.json", "build_request", gitlab_cfg(f) + [
        ("selectedRaw", "string", f.out(select, "selectedRaw")), ("mappingsRaw", "string", f.out(select, "mappingsRaw")),
        ("description", "string", f.out(select, "description")),
        ("source", "object", P("sourceInventory")), ("target", "object", P("targetInventory")),
        ("sourceRegion", "string", P("sourceRegion")), ("targetRegion", "string", P("targetRegion")),
        ("userId", "string", P("userId") if signin else "")],
        {"state": "string", "error": "string", "requestId": "string", "requestJson": "string", "gitlabApi": "string",
         "previewText": "string", "summaryText": "string"})
    f.link(select, build)
    build_ok = f.equals("Request valid?", f.out(build, "state"), "ok")
    f.link(build, build_ok)
    vstart = f.fn("Resources to check for promotion variables", "vars_start", [
        ("requestJson", "string", f.out(build, "requestJson"))],
        {"state": "string", "error": "string", "ids": "array", "firstId": "string"})
    f.link(build_ok, vstart, false_target=f.fail("Invalid request", build))
    vstart_ok = f.equals("Selection readable?", f.out(vstart, "state"), "ok")
    f.link(vstart, vstart_ok)
    decl_init = f.save("Start declarations", [("declFound", "object", "[]"), ("declIdx", "number", "0"),
                                              ("declId", "string", [f.out(vstart, "firstId")])])
    f.link(vstart_ok, decl_init, false_target=f.fail("Invalid request", vstart))
    # Built back-to-front from here so each block knows its continuation.
    submit_go_title = "Submit pressed?"
    preview = f.page("Review request", page_html(
        "Review the promotion request",
        "{{local.BUILD.payload.output.summaryText}} Secret values are redacted. Check the configuration below; reviewers see the same "
        "file in the merge request.",
        [("Promotion variables: {{local.VARS.payload.output.summaryText}}", "{{local.VARS.payload.output.reportText}}"),
         ("requests/{{local.BUILD.payload.output.requestId}}/request.json", "{{local.BUILD.payload.output.previewText}}")],
        "previewForm", "", [("SUBMIT", "Submit to GitLab", "btn-primary"), ("CANCEL", "Cancel", "btn-outline-secondary")],
        step=3, route="global.parameters"), ["buttonValue"])
    vars_report = f.fn("Promotion variables ready?", "vars_report", [
        ("requestJson", "string", f.out(build, "requestJson")),
        ("declarations", "object", f.fv("declFound")),
        ("sourceDefinitionPages", "object", f.fv("srcDefsPages")), ("sourceDefinitionError", "string", f.fv("srcDefsError")),
        ("targetDefinitionPages", "object", f.fv("tgtDefsPages")), ("targetDefinitionError", "string", f.fv("tgtDefsError"))],
        {"state": "string", "error": "string", "open": "number", "count": "number", "summaryText": "string", "reportText": "string",
         "variablesMd": "string"})
    f.link(vars_report, preview)
    submit_go = f.equals(submit_go_title, f.out(preview, "buttonValue"), "SUBMIT")
    f.link(preview, submit_go)
    done = f.success("Return the request", [
        ("requestJson", "string", f.out(build, "requestJson")), ("requestId", "string", f.out(build, "requestId")),
        ("gitlabApi", "string", f.out(build, "gitlabApi")), ("variablesMd", "string", f.out(vars_report, "variablesMd"))])
    f.link(submit_go, done, false_target=f.message("Cancelled", ["Cancelled. Nothing was sent to GitLab."]))
    tgt_defs = f.pages("tgtDefs", None, [f.s("apiOrigin"), "/v1/environments/", tgt, "/promotionVariableDefinitions"],
                       "variableDefinitions", vars_report, soft=True)
    src_defs = f.pages("srcDefs", None, [f.s("apiOrigin"), "/v1/environments/", src, "/promotionVariableDefinitions"],
                       "variableDefinitions", tgt_defs, soft=True)
    decl_get = f.rest("Read promotion-variable declaration", "GET",
                      [f.s("apiOrigin"), "/v1/environments/", src, "/promotionVariableDeclarations/", f.fv("declId")], f.bearer())
    decl_col = f.fn("Keep declaration", "decl_collect", [
        ("ids", "object", f.out(vstart, "ids")), ("found", "object", f.fv("declFound")),
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
    f.link(decl_init, decl_get)
    f.alias = {"VARS": vars_report, "BUILD": build}
    return f


SUBMIT_INPUTS = [("gitlabUrl", "string"), ("gitlabProjectId", "string"), ("gitlabEnvironment", "string"),
                 ("gitlabApi", "string"), ("requestJson", "string"), ("variablesMd", "string"),
                 ("username", "string"), ("userId", "string")]


def build_submit_flow():
    """Subflow 4: check the project and default branch, create promotion/<id>, commit request.json, open the MR."""
    f = Flow("PingOne Promote - 4 Submit to GitLab", FLOW_IDS["submit"],
             "Subflow of PingOne Promote - Request: commits request.json to a promotion branch and opens the merge request, "
             "exactly as the Go app does. Returns the merge request URL.",
             letter="d", inputs=SUBMIT_INPUTS)
    api = P("gitlabApi")
    intro = f.wait_page("Submitting to GitLab", "Checking the promotion project, then committing the request and opening the merge request.", 3)
    proj_get = f.rest("GitLab: read project", "GET", [api], gitlab_headers())
    f.link(intro, proj_get)
    proj = f.fn("GitLab project private and as configured?", "gitlab_project", gitlab_cfg(f) + [
        ("statusCode", "number", f.out(proj_get, "rawResponse.statusCode")), ("body", "object", f.out(proj_get, "rawResponse.body"))],
        {"state": "string", "error": "string", "defaultBranch": "string", "defaultBranchPath": "string"})
    f.link(proj_get, proj)
    proj_ok = f.equals("Project ok?", f.out(proj, "state"), "ok")
    f.link(proj, proj_ok)
    br_get = f.rest("GitLab: read default branch", "GET", [api, "/repository/branches/", f.out(proj, "defaultBranchPath")], gitlab_headers())
    f.link(proj_ok, br_get, false_target=f.fail("GitLab project check failed", proj))
    bodies = f.fn("Default branch protected? Build GitLab payloads", "gitlab_branch", gitlab_cfg(f) + [
        ("defaultBranch", "string", f.out(proj, "defaultBranch")),
        ("statusCode", "number", f.out(br_get, "rawResponse.statusCode")), ("body", "object", f.out(br_get, "rawResponse.body")),
        ("requestJson", "string", P("requestJson")), ("username", "string", P("username")),
        ("variablesMd", "string", P("variablesMd")), ("userId", "string", P("userId"))],
        {"state": "string", "error": "string", "branch": "string", "branchBody": "string", "commitBody": "string", "mrBody": "string"})
    f.link(br_get, bodies)
    bodies_ok = f.equals("Branch protected?", f.out(bodies, "state"), "ok")
    f.link(bodies, bodies_ok)
    branch_post = f.rest("GitLab: create promotion branch", "POST", [api, "/repository/branches"], gitlab_headers(True),
                         raw=[f.out(bodies, "branchBody")])
    f.link(bodies_ok, branch_post, false_target=f.fail("GitLab project check failed", bodies))
    branch_ok_fn = f.fn("Branch created?", "gitlab_step", [
        ("what", "string", "create promotion branch"), ("statusCode", "number", f.out(branch_post, "rawResponse.statusCode"))],
        {"state": "string", "error": "string"})
    f.link(branch_post, branch_ok_fn)
    branch_ok = f.equals("Branch ok?", f.out(branch_ok_fn, "state"), "ok")
    f.link(branch_ok_fn, branch_ok)
    commit = f.rest("GitLab: commit request.json", "POST", [api, "/repository/commits"], gitlab_headers(True),
                    raw=[f.out(bodies, "commitBody")])
    f.link(branch_ok, commit, false_target=f.fail("Could not create the promotion branch", branch_ok_fn))
    commit_ok_fn = f.fn("Commit created?", "gitlab_step", [
        ("what", "string", "commit promotion request"), ("statusCode", "number", f.out(commit, "rawResponse.statusCode"))],
        {"state": "string", "error": "string"})
    f.link(commit, commit_ok_fn)
    commit_ok = f.equals("Commit ok?", f.out(commit_ok_fn, "state"), "ok")
    f.link(commit_ok_fn, commit_ok)
    mr_post = f.rest("GitLab: open merge request", "POST", [api, "/merge_requests"], gitlab_headers(True),
                     raw=[f.out(bodies, "mrBody")])
    f.link(commit_ok, mr_post, false_target=f.fail("Could not commit the request", commit_ok_fn))
    mr_ok_fn = f.fn("Merge request opened?", "gitlab_mr", gitlab_cfg(f) + [
        ("statusCode", "number", f.out(mr_post, "rawResponse.statusCode")), ("body", "object", f.out(mr_post, "rawResponse.body"))],
        {"state": "string", "error": "string", "webUrl": "string", "webUrlText": "string", "iid": "number"})
    f.link(mr_post, mr_ok_fn)
    mr_ok = f.equals("MR ok?", f.out(mr_ok_fn, "state"), "ok")
    f.link(mr_ok_fn, mr_ok)
    done = f.success("Return the merge request", [("webUrlText", "string", f.out(mr_ok_fn, "webUrlText"))])
    f.link(mr_ok, done, false_target=f.fail("Could not open the merge request", mr_ok_fn))
    return f


def build_flows(signin):
    """The main flow (what the application's policy launches) and its four UI subflows, in import order."""
    envs, snap, req, submit = build_envs_flow(), build_snapshot_flow(), build_request_subflow(signin), build_submit_flow()
    f = Flow("PingOne Promote - Request", FLOW_IDS["main"],
             "A DaVinci Admin selects resources from live snapshots; the flow builds request.json exactly as the Go app does and "
             "opens a GitLab merge request. Deployment and rollback happen only in the protected GitLab pipeline. "
             "Steps run in four subflows (1 Choose environments, 2 Snapshot environment, 3 Choose resources and review, "
             "4 Submit to GitLab).")
    # First: check the admin-set flow variables, naming any placeholder or typo.
    settings_check = f.fn("Check settings", "check_settings",
                          [(k, "string", f.fv(k)) for k in ("apiOrigin", "authOrigin", "hostEnvironmentId", "gitlabUrl", "gitlabProjectId")] +
                          [(f"env{i}{k}", "string", f.fv(f"env{i}{k}")) for i in range(1, 5) for k in ("Name", "Id", "GitlabEnvironment")] +
                          [("gitlabEnvironment", "string", f.fv("gitlabEnvironment"))],
                          {"state": "string", "error": "string"})
    settings_ok = f.equals("Settings ok?", f.out(settings_check, "state"), "ok")
    f.link(settings_check, settings_ok)
    if signin:
        start, allow, denied = f.sign_in()
        who = lambda field: f.out(f.authz, field)
    else:
        start = f.worker_token()
        who = lambda field: ""
    f.link(settings_ok, start, false_target=f.fail("Settings need attention", settings_check))
    token = f.out(f.token, "rawResponse.body.access_token")
    c_envs = f.conductor("1 Choose environments", envs, {k: f.fv(k) for k, _ in ENV_SLOT_INPUTS})
    if signin:
        f.link(allow, c_envs, false_target=denied)
    else:
        f.link(start, c_envs)
    keep = f.save("Keep chosen environments", [(k, "string", [f.out(c_envs, k)]) for k in
                                               ("sourceEnvironmentId", "sourceEnvName", "targetEnvironmentId", "targetEnvName",
                                                "targetGitlabEnvironment")])
    f.link(c_envs, keep)
    c_src = f.conductor("2 Snapshot source environment", snap, {
        "apiOrigin": f.fv("apiOrigin"), "accessToken": token, "environmentId": f.fv("sourceEnvironmentId"),
        "environmentLabel": f.fv("sourceEnvName")})
    f.link(keep, c_src)
    c_tgt = f.conductor("2 Snapshot target environment", snap, {
        "apiOrigin": f.fv("apiOrigin"), "accessToken": token, "environmentId": f.fv("targetEnvironmentId"),
        "environmentLabel": f.fv("targetEnvName")})
    f.link(c_src, c_tgt)
    # As the local app does: read PingOne's saved promotion mappings for this pair (a failure is only a warning).
    saved_maps = f.rest("Saved promotion mappings", "GET", [f.fv("apiOrigin"), "/v1/environments/", f.fv("sourceEnvironmentId"),
                                                            "/promotionMappings/", f.fv("targetEnvironmentId")], f.bearer())
    f.link(c_tgt, saved_maps)
    inv = f.fn("Live inventories", "inventories", [
        ("mappingsStatus", "number", f.out(saved_maps, "rawResponse.statusCode")),
        ("mappingsBody", "object", f.out(saved_maps, "rawResponse.body")),
        ("apiOrigin", "string", f.fv("apiOrigin")), ("pinned", "string", "false"),
        ("sourceEnvironmentId", "string", f.fv("sourceEnvironmentId")), ("targetEnvironmentId", "string", f.fv("targetEnvironmentId")),
        ("sourceEnvBody", "object", f.out(c_src, "envBody")), ("sourceSnapshotBody", "object", f.out(c_src, "snapshotBody")),
        ("sourcePages", "object", f.out(c_src, "pages")), ("sourceSnapshotId", "string", f.out(c_src, "snapshotId")),
        ("targetEnvBody", "object", f.out(c_tgt, "envBody")), ("targetSnapshotBody", "object", f.out(c_tgt, "snapshotBody")),
        ("targetPages", "object", f.out(c_tgt, "pages")), ("targetSnapshotId", "string", f.out(c_tgt, "snapshotId"))],
        {"state": "string", "error": "string", "source": "object", "target": "object", "sourceListText": "string", "targetListText": "string",
         "pickerDataText": "string"})
    f.link(saved_maps, inv)
    inv_ok = f.equals("Live inventories ok?", f.out(inv, "state"), "ok")
    f.link(inv, inv_ok)
    c_req = f.conductor("3 Choose resources and review", req, {
        "apiOrigin": f.fv("apiOrigin"), "accessToken": token,
        "sourceEnvironmentId": f.fv("sourceEnvironmentId"), "targetEnvironmentId": f.fv("targetEnvironmentId"),
        "sourceEnvName": f.fv("sourceEnvName"), "targetEnvName": f.fv("targetEnvName"),
        "sourceInventory": f.out(inv, "source"), "targetInventory": f.out(inv, "target"),
        "sourceListText": f.out(inv, "sourceListText"), "targetListText": f.out(inv, "targetListText"),
        "pickerData": f.out(inv, "pickerDataText"),
        "sourceRegion": f.fv("sourceRegion"), "targetRegion": f.fv("targetRegion"),
        "gitlabUrl": f.fv("gitlabUrl"), "gitlabProjectId": f.fv("gitlabProjectId"), "gitlabEnvironment": f.fv("targetGitlabEnvironment"),
        "userId": who("userId"), "usernameText": who("usernameText")})
    f.link(inv_ok, c_req, false_target=f.fail("Could not read the environments", inv))
    c_sub = f.conductor("4 Submit to GitLab", submit, {
        "gitlabUrl": f.fv("gitlabUrl"), "gitlabProjectId": f.fv("gitlabProjectId"), "gitlabEnvironment": f.fv("targetGitlabEnvironment"),
        "gitlabApi": f.out(c_req, "gitlabApi"), "requestJson": f.out(c_req, "requestJson"), "variablesMd": f.out(c_req, "variablesMd"),
        "username": who("username"), "userId": who("userId")})
    f.link(c_req, c_sub)
    done = f.page("Merge request opened", page_html(
        "Promotion request submitted",
        "Nothing has changed in PingOne yet. After the merge request is approved and merged, the GitLab pipeline plans the "
        "promotion; deployment needs its own approval there.",
        [], "doneForm", "", [], step=4, route=True,
        note='<div class="pp-note ok">Merge request opened: <a href="{{local.MRDONE.payload.output.webUrlText}}" target="_blank" '
             'rel="noopener">{{local.MRDONE.payload.output.webUrlText}}</a></div>'), [])
    f.link(c_sub, done)
    f.alias = {"MRDONE": c_sub}
    return [f, envs, snap, req, submit]


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


TF_NAME = {FLOW_IDS["main"]: "request", FLOW_IDS["envs"]: "choose_environments", FLOW_IDS["snap"]: "snapshot",
           FLOW_IDS["request"]: "choose_resources", FLOW_IDS["submit"]: "submit"}


def terraform(flow_datas):
    """flow.tf.json: every flow, its flow variables, and deploy/enable for the subflows
    (main.tf deploys the main flow). Subflow references point at the Terraform-managed flows."""
    slot_of = {cid: slot for slot, (_, cid, _) in CONN.items()}
    flows, variables, deploys, enables = {}, {}, {}, {}
    for flow_data in flow_datas:
        res_name = TF_NAME[flow_data["flowId"]]
        nodes = {}
        for n in flow_data["graphData"]["elements"]["nodes"]:
            d = n["data"]
            data = {"node_type": d["nodeType"], "label": d["label"]}
            if d["nodeType"] == "CONNECTION":
                data.update({"connection_id": "${local.connection_ids.%s}" % slot_of[d["connectionId"]], "connector_id": d["connectorId"],
                             "name": d["name"], "status": d["status"], "capability_name": d["capabilityName"], "type": d["type"]})
            if d.get("properties"):
                props = tf_literal(json.dumps(d["properties"], separators=(",", ":")))
                for fid, rname in TF_NAME.items():
                    props = props.replace('"value":"%s"}' % fid, '"value":"${pingone_davinci_flow.%s.id}"}' % rname)
                data["properties"] = props
            nodes[d["id"]] = {"data": data, "position": n["position"], "group": "nodes", "removed": False, "selected": False,
                              "selectable": True, "locked": False, "grabbable": True, "pannable": False}
        edges = {e["data"]["id"]: {"data": {"source": e["data"]["source"], "target": e["data"]["target"]}, "position": {"x": 0, "y": 0},
                                   "group": "edges", "removed": False, "selected": False, "selectable": True, "locked": False,
                                   "grabbable": True, "pannable": True}
                 for e in flow_data["graphData"]["elements"]["edges"]}
        flow = {"environment_id": "${var.host_environment_id}", "name": flow_data["name"], "description": flow_data["description"],
                "color": flow_data["flowColor"],
                "settings": {"log_level": FLOW_SETTINGS["logLevel"], "csp": FLOW_SETTINGS["csp"],
                             "flow_timeout_in_seconds": FLOW_SETTINGS["flowTimeoutInSeconds"],
                             "flow_http_timeout_in_seconds": FLOW_SETTINGS["flowHttpTimeoutInSeconds"]},
                "graph_data": {"elements": {"nodes": nodes, "edges": edges}, "zooming_enabled": True, "user_zooming_enabled": True, "zoom": 1,
                               "min_zoom": 1e-50, "max_zoom": 1e50, "panning_enabled": True, "user_panning_enabled": True,
                               "pan": {"x": 0, "y": 0}, "box_selection_enabled": True, "renderer": '{"name":"null"}'}}
        if flow_data.get("inputSchema"):
            flow["input_schema"] = [{"property_name": s["propertyName"], "preferred_data_type": s["preferredDataType"],
                                     "preferred_control_type": s["preferredControlType"], "required": s["required"],
                                     "is_expanded": s["isExpanded"], "description": s["description"]} for s in flow_data["inputSchema"]]
        if flow_data.get("outputSchemaCompiled"):
            o = flow_data["outputSchemaCompiled"]["output"]
            flow["output_schema"] = {"output": {"type": o["type"], "additional_properties": o["additionalProperties"],
                                                "properties": json.dumps(o["properties"], separators=(",", ":"))}}
        flows[res_name] = flow
        for v in flow_data["variables"]:
            if v["context"] != "flow":
                continue
            name = v["fields"]["displayName"]
            res = {"environment_id": "${var.host_environment_id}", "context": "flow", "name": name, "display_name": name,
                   "data_type": v["fields"]["type"], "mutable": True, "flow": {"id": "${pingone_davinci_flow.%s.id}" % res_name}}
            if name in TF_SETTING and res_name == "request":
                res["value"] = {"string": TF_SETTING[name]}
            key = ("flow_" if res_name == "request" else f"{res_name}_") + re.sub(r"[^A-Za-z0-9_]", "_", name)
            variables[key] = res
        if res_name != "request":
            deploys[res_name] = {"environment_id": "${var.host_environment_id}", "flow_id": "${pingone_davinci_flow.%s.id}" % res_name,
                                 "deploy_trigger_values": {"graph": "${sha256(jsonencode(pingone_davinci_flow.%s.graph_data))}" % res_name},
                                 "depends_on": ["pingone_davinci_variable.worker_basic_auth", "pingone_davinci_variable.gitlab_token"]}
            enables[res_name] = {"environment_id": "${var.host_environment_id}", "flow_id": "${pingone_davinci_flow.%s.id}" % res_name,
                                 "enabled": True}
    return {"locals": {"flow_has_signin": SIGNIN},
            "resource": {"pingone_davinci_flow": flows, "pingone_davinci_variable": variables,
                         "pingone_davinci_flow_deploy": deploys, "pingone_davinci_flow_enable": enables}}


def export_file(flows):
    """One import file holding the main flow and its subflows, as DaVinci exports a flow with subflows."""
    datas = [resolve_aliases(fl) for fl in flows]
    datas[0]["parentFlowId"] = datas[0]["flowId"]
    return {"flows": datas, "companyId": COMPANY, "customerId": "00000000000000000000000000000000"}


OUT.mkdir(parents=True, exist_ok=True)
FLOWS = build_flows(SIGNIN)
if TERRAFORM:
    datas = [resolve_aliases(fl) for fl in FLOWS]
    (OUT / "flow.tf.json").write_text(json.dumps(terraform(datas), indent=1) + "\n")
    print(f"flow.tf.json: {len(datas)} flows, {sum(len(d['graphData']['elements']['nodes']) for d in datas)} nodes, "
          f"{sum(1 for d in datas for v in d['variables'] if v['context'] == 'flow')} flow variables")
    sys.exit(0)
data = export_file(FLOWS)
fname = "pingone-promote-request.flow.json"
(OUT / fname).write_text(json.dumps(data, separators=(",", ":")) + "\n")
for d in data["flows"]:
    steps = sum(1 for n in d["graphData"]["elements"]["nodes"] if n["data"]["nodeType"] == "CONNECTION")
    print(f"{fname}: {d['name']}: {steps} steps, {len(d['graphData']['elements']['edges'])} edges")
