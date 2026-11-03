"""Validate the PingOne Promote app flow export and scan the repository for secrets and IDs.

usage: python3 check_app.py [EXPORT.json] [--repo DIR] [--allow-connection-ids]

Structure (as ../davinci-feasibility/check_flow.py, adapted to the app):
- JSON parses; one flow in the {"flows": [...]} wrapper with the export fields
- unique 10-character node IDs; every edge endpoint exists; the sign-in
  subflow is the one start node; every node reachable; evaluator false
  branches are real edges; only screens end a path
- every {{local.<node>...}} reference names a node that can run before it, and
  a function output it reads is declared in that function's output schema
- every flow-instance variable read is declared and written by an earlier
  Variables node; every flow and company variable read is declared
- custom functions use the DaVinci wrapper, read exactly the inputs wired to
  them, and every routed action is one the code can return
- no <style>/<script> inside Custom HTML
Trust boundary (DESIGN.md, "Trust boundary"):
- two company secrets, both empty secrets: the worker credential and the
  GitLab project token
- configuration is COMPANY-scoped (kept across re-imports), declared without
  values for the IDs and groups; per-session state is flow-INSTANCE scoped.
  A state variable that is company-scoped (shared by every user) or a
  configuration setting that is flow-scoped (reset on re-import) fails
- the worker credential is used only by the PingOne token call, the access
  token only by the PingOne list call, the GitLab token only by GitLab calls
- no secret or token reference in any function input, screen or saved value
- PingOne calls: one POST to the token endpoint, every other call a GET
- GitLab calls: one POST (the request-file commit), GETs (the job log read is one of them), one DELETE; the
  function code allows only the paths in lib.gl (checked by test_app.js)
Hygiene (export and every text file in the repo):
- no UUIDs except the all-zero placeholder, no 32-hex IDs except the known
  placeholders, no bearer/basic credentials, GitLab tokens, JWTs or private keys
- ID, group and secret settings are empty in the export
"""
import argparse
import collections
import json
import os
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
DEFAULT = HERE / "flow" / "pingone-promote-app.flow.json"
ZERO_UUID = "00000000-0000-0000-0000-000000000000"
ALLOWED_HEX = ({"0" * 32, "f0d00a7e00000000000000000000fe01", "f0d00a7e00000000000000000000fe10", "f0d00a7e00000000000000000000fe11"}
               | {f"a1f0d00a00000000000000000000000{i}" for i in range(10)})
REST_KEYS = {"url", "httpMethod", "headers", "httpBody", "raw", "nodeTitle"}
SCREEN = "customHTMLTemplate"
CAPS = {"customFunction", "AEqualsB", "saveValue", "makeRestApiCall", SCREEN, "startUiSubFlow"}
EMPTY_SETTINGS = {"ppWorkerEnvironmentId", "ppSourceEnvs", "ppGitlabProjectId", "ppAllowedGroups"}
SECRETS = {"ppWorkerBasicAuth", "ppGitlabToken"}
# Per-session state: flow-instance variables only. Company variables are shared by every user and session.
STATE = {"ppState", "ppFlows", "ppUrl", "ppBody", "ppForm", "ppMain", "ppLastFailed"}


def setting_names():
    """The operator settings, from the app's own list (fn/_lib.js lib.SETTINGS), as pp<Name>."""
    m = re.search(r"^lib\.SETTINGS = \[(.*?)\];", (HERE / "fn" / "_lib.js").read_text(encoding="utf-8"), re.M | re.S)
    return {"pp" + n[0].upper() + n[1:] for n in re.findall(r"'([A-Za-z]+)'", m.group(1))}


SETTINGS = setting_names()
SCANS = [
    ("UUID", re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")),
    ("32-hex ID", re.compile(r"(?<![0-9a-fA-F])[0-9a-fA-F]{32}(?![0-9a-fA-F])")),
    ("bearer/basic credential", re.compile(r"\b(?:Bearer|Basic)\s+(?!\{\{|<|\$|\"|'|\+|\(|\[)[A-Za-z0-9._~+/=-]{16,}")),
    ("GitLab token", re.compile(r"gl(?:pat|ptt|rt|dt|oas|cbt|ft)-[A-Za-z0-9_-]{16,}")),
    ("JWT", re.compile(r"eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.")),
    ("private key", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
    ("client secret literal", re.compile(r"client_secret=(?!\$|\{|<)[A-Za-z0-9._~-]{8,}")),
]


def scan_text(label, text, problems):
    for name, rx in SCANS:
        for m in rx.finditer(text):
            v = m.group(0)
            if name == "UUID" and v == ZERO_UUID:
                continue
            if name == "32-hex ID" and v.lower() in ALLOWED_HEX:
                continue
            problems.append(f"{label}: {name} found ({v[:12]}...)")


def check_export(path, allow_conn_ids):
    problems = []
    text = pathlib.Path(path).read_text(encoding="utf-8")
    data = json.loads(text)
    flows = data.get("flows")
    if not isinstance(flows, list) or len(flows) != 1 or "companyId" not in data or "customerId" not in data:
        return [f"{path}: expected {{flows: [one flow], companyId, customerId}}"], "unreadable"
    f = flows[0]
    for k in ("companyId", "flowId", "name", "graphData", "settings", "variables", "connectorIds", "currentVersion", "publishedVersion", "flowStatus"):
        if k not in f:
            problems.append(f"flow lacks {k}")
    if f.get("companyId") != ZERO_UUID:
        problems.append("companyId must be the all-zero placeholder")
    els = f["graphData"]["elements"]
    nodes = {n["data"]["id"]: n["data"] for n in els["nodes"]}
    edges = [e["data"] for e in els["edges"]]
    if len(nodes) != len(els["nodes"]):
        problems.append("duplicate node IDs")
    for i in nodes:
        if not re.fullmatch(r"[a-z0-9]{10}", i):
            problems.append(f"bad node id {i}")
    succ, pred = collections.defaultdict(set), collections.defaultdict(set)
    for e in edges:
        if e["source"] not in nodes or e["target"] not in nodes:
            problems.append(f"dangling edge {e['id']}")
            continue
        succ[e["source"]].add(e["target"])
        pred[e["target"]].add(e["source"])
    starts = [i for i in nodes if not pred[i]]
    if len(starts) != 1 or nodes[starts[0]].get("capabilityName") != "startUiSubFlow":
        problems.append(f"expected the sign-in subflow as the one start node, found {starts}")
    if sum(1 for n in nodes.values() if n.get("capabilityName") == "startUiSubFlow") != 1:
        problems.append("expected exactly one subflow node (sign-in)")
    seen, q = set(), list(starts[:1])
    while q:
        i = q.pop()
        if i not in seen:
            seen.add(i)
            q.extend(succ[i])
    if seen != set(nodes):
        problems.append(f"unreachable nodes: {sorted(set(nodes) - seen)[:5]}")
    connectors = {n["connectorId"] for n in nodes.values() if n["nodeType"] == "CONNECTION"}
    if set(f["connectorIds"]) != connectors:
        problems.append("connectorIds does not list exactly the connectors used")

    def title(i):
        return nodes[i].get("properties", {}).get("nodeTitle", {}).get("value", i)

    def ancestors(i):
        out, q = set(), list(pred[i])
        while q:
            x = q.pop()
            if x not in out:
                out.add(x)
                q.extend(pred[x])
        return out

    declared = collections.defaultdict(dict)
    for v in f["variables"]:
        declared[v["context"]][v["name"].split("##SK##")[0]] = v
        if v["context"] == "flow" and not v["name"].endswith("##SK##flow##SK##" + f["flowId"]):
            problems.append(f"flow variable {v['name']} not bound to this flow")
    for v in f["variables"]:
        base = v["name"].split("##SK##")[0]
        if base in STATE and v["context"] != "flowInstance":
            problems.append(f"state variable {base} is {v['context']}-scoped; state must be flow-instance (company variables are shared by every user)")
        if base in SETTINGS and v["context"] != "company":
            problems.append(f"setting {base} is {v['context']}-scoped; settings must be company variables (a re-import resets flow variables)")
    if set(declared["company"]) != SECRETS | SETTINGS:
        problems.append(f"company variables must be exactly the two secrets and the settings; differences: {sorted(set(declared['company']) ^ (SECRETS | SETTINGS))}")
    if declared["flow"]:
        problems.append(f"flow variables are not used (configuration is company-scoped): {sorted(declared['flow'])}")
    for name, v in declared["company"].items():
        if name in SECRETS and (v["fields"].get("type") != "secret" or v["fields"].get("value") not in ("", None)):
            problems.append(f"company variable {name} must be an empty secret")
        if name in SETTINGS and v["fields"].get("type") != "string":
            problems.append(f"company setting {name} must be a string")
    for name in EMPTY_SETTINGS:
        if declared["company"].get(name, {}).get("fields", {}).get("value", "") != "":
            problems.append(f"{name} must be empty in the export (set it in DaVinci)")
    written = collections.defaultdict(set)
    outputs = {}
    token_nodes = []
    for i, n in nodes.items():
        cap = n.get("capabilityName")
        p = n.get("properties", {})
        if n["nodeType"] == "CONNECTION" and cap not in CAPS:
            problems.append(f"{i} uses capability {cap}, which the app does not expect")
        if cap == "saveValue":
            for item in p["saveVariables"]["value"]:
                written[item["name"]].add(i)
                if item["name"] not in declared["flowInstance"]:
                    problems.append(f"{i} saves undeclared flow-instance variable {item['name']}")
        if cap == "customFunction":
            outputs[i] = set(json.loads(p["outputSchema"]["value"])["properties"])
        if cap == "makeRestApiCall" and "ppWorkerBasicAuth" in json.dumps(p.get("headers", {})):
            token_nodes.append(i)
    if len(token_nodes) != 1:
        problems.append("expected exactly one PingOne token call")
    methods = collections.Counter()
    for i, n in nodes.items():
        p = n.get("properties", {})
        cap = n.get("capabilityName")
        blob = json.dumps(p)
        anc = ancestors(i)
        for ref, field in set(re.findall(r"local\.([a-z0-9]{10})\.payload\.output\.([A-Za-z0-9_]+)", blob)):
            if ref not in nodes:
                problems.append(f"{i} references missing node {ref}")
            elif ref not in anc:
                problems.append(f"{i} ({title(i)}) references {ref} which cannot run before it")
            elif ref in outputs and field not in outputs[ref]:
                problems.append(f"{i} ({title(i)}) reads {field} which {title(ref)} does not declare")
        for var in set(re.findall(r"global\.variables\.([A-Za-z0-9_]+)", blob)):
            if var not in declared["flowInstance"]:
                problems.append(f"{i} reads undeclared flow-instance variable {var}")
            elif not (written[var] & anc):
                problems.append(f"{i} ({title(i)}) reads {var} before any node writes it")
        if re.search(r"global\.flow\.variables\.", blob):
            problems.append(f"{i} ({title(i)}) reads a flow-scoped variable; configuration is company-scoped, state is flow-instance")
        for ctx, rx in (("flow", r"global\.flow\.variables\.([A-Za-z0-9_]+)"), ("company", r"global\.company\.variables\.([A-Za-z0-9_]+)")):
            for var in set(re.findall(rx, blob)):
                if var not in declared[ctx]:
                    problems.append(f"{i} reads undeclared {ctx} variable {var}")
        # Trust boundary: secrets and the access token appear only in REST headers.
        if cap != "makeRestApiCall":
            for var in set(re.findall(r"global\.company\.variables\.([A-Za-z0-9_]+)", blob)):
                # Settings (never secrets) go to the sign-in check, the one function that builds the configuration.
                if var in SECRETS or var not in SETTINGS or cap != "customFunction":
                    problems.append(f"{i} ({title(i)}) reads company variable {var} outside a REST call header")
            if "access_token" in blob and cap != "customFunction":
                problems.append(f"{i} ({title(i)}) reads the PingOne access token")
            if cap == "customFunction" and any("access_token" in x["value"] for x in p["variableInputList"]["value"]):
                problems.append(f"{i} ({title(i)}) is given the PingOne access token")
        if n["nodeType"] == "EVAL":
            if not succ[i] or len(pred[i]) != 1:
                problems.append(f"evaluator {i} must have one source and at least one target")
            for t in p or {}:
                if t not in succ[i]:
                    problems.append(f"evaluator {i} false branch {t} is not an edge")
            continue
        if not succ[i] and cap != SCREEN:
            problems.append(f"non-terminal dead end {i} ({title(i)})")
        if cap == SCREEN:
            if re.search(r"<(style|script)\b", p["customHTML"]["value"], re.I):
                problems.append(f"{i} ({title(i)}) has <style>/<script> in its HTML; use customCSS/customScript")
            if not succ[i] and p["formFieldsList"]["value"]:
                problems.append(f"{i} ({title(i)}) is an end screen but has form fields")
        if cap == "makeRestApiCall":
            if set(p) - REST_KEYS:
                problems.append(f"{i} REST node has unknown properties {sorted(set(p) - REST_KEYS)}")
            method = p.get("httpMethod", {}).get("value")
            if ("raw" in p) != (p.get("httpBody", {}).get("value") == "raw"):
                problems.append(f"{i} REST body: httpBody must be 'raw' exactly when raw is set")
            evs = [e for e in succ[i] if nodes[e]["nodeType"] == "EVAL"]
            if len(evs) != 1 or not any(v.get("value") == "allTriggersFalse" for v in (nodes[evs[0]].get("properties") or {}).values()):
                problems.append(f"{i} ({title(i)}) REST call has no failure branch")
            url_kids = json.loads(p["url"]["value"])[0]["children"]
            if len(url_kids) != 3 or url_kids[1].get("data") != "{{global.variables.ppUrl}}" or any(k.get("text") for k in url_kids):
                problems.append(f"{i} ({title(i)}) URL must be exactly ppUrl (built and checked by a function)")
            hdrs = json.dumps(p["headers"]["value"])
            body = p.get("raw", {}).get("value", "")
            is_gitlab = title(i).startswith("GitLab:")
            is_pingone = title(i).startswith("PingOne:")
            if is_gitlab == is_pingone:
                problems.append(f"{i} ({title(i)}) must be a PingOne: or GitLab: call")
            if "global.company.variables" in body:
                problems.append(f"{i} ({title(i)}) puts a secret in a request body")
            if is_gitlab:
                methods["gitlab " + method] += 1
                if "ppGitlabToken" not in hdrs or "ppWorkerBasicAuth" in hdrs or "access_token" in hdrs:
                    problems.append(f"{i} GitLab calls send the GitLab token and nothing else")
                if method == "POST" and body != "{{global.variables.ppBody}}":
                    problems.append(f"{i} the GitLab POST body must be exactly ppBody (built by Handle input)")
                if method not in ("GET", "POST", "DELETE"):
                    problems.append(f"{i} GitLab method {method} not allowed")
            if is_pingone:
                methods["pingone " + method] += 1
                if "ppGitlabToken" in hdrs:
                    problems.append(f"{i} GitLab token sent to PingOne")
                if i in token_nodes:
                    if method != "POST" or body != "grant_type=client_credentials":
                        problems.append(f"{i} the token call must POST grant_type=client_credentials")
                else:
                    if method != "GET":
                        problems.append(f"{i} ({title(i)}) PingOne calls other than the token must be GET (read-only)")
                    m = re.search(r"local\.([a-z0-9]{10})\.payload\.output\.rawResponse\.body\.access_token", hdrs)
                    if not m or m.group(1) not in token_nodes or "ppWorkerBasicAuth" in hdrs:
                        problems.append(f"{i} PingOne reads must send only the bearer token from the token call")
        if cap == "customFunction":
            code = p["code"]["value"]
            if not code.startswith("module.exports = a = async ({params}) => {\nconst contract = (() => {") or "'use strict'" in code:
                problems.append(f"{i} function wrapper wrong")
            step = code.split("// ---- step: ", 1)[-1]
            read = set(re.findall(r"params\.([A-Za-z0-9_]+)", step))
            given = {x["name"] for x in p["variableInputList"]["value"]}
            if read - given:
                problems.append(f"{i} ({title(i)}) reads params not wired: {sorted(read - given)}")
            dynamic = re.search(r"\(params\)|params\[", step)
            if not dynamic and given - read:
                problems.append(f"{i} ({title(i)}) wires unused inputs: {sorted(given - read)}")
        if cap == "AEqualsB":
            m = re.search(r"local\.([a-z0-9]{10})\.payload\.output\.action", json.dumps(p["leftValueA"]["value"]))
            if m:
                value = json.loads(p["rightValueB"]["value"])[0]["children"][0]["text"]
                code = nodes[m.group(1)]["properties"]["code"]["value"]
                if f"'{value}'" not in code:
                    problems.append(f"{i} routes on action '{value}' which {title(m.group(1))} never returns")
    want = collections.Counter({"pingone POST": 1, "pingone GET": 1, "gitlab POST": 1, "gitlab GET": 2, "gitlab DELETE": 1})
    if methods != want:
        problems.append(f"REST calls by method are {dict(methods)}, expected {dict(want)}")
    scanned = text
    if allow_conn_ids:
        scanned = re.sub(r'"(connectionId|value)": "[0-9a-f]{32}"', '"\\1": "x"', scanned)
    scan_text(pathlib.Path(path).name, scanned, problems)
    sizes = [len(n["properties"]["code"]["value"]) for n in nodes.values() if n.get("capabilityName") == "customFunction"]
    stats = (f"{len(nodes)} nodes ({sum(1 for n in nodes.values() if n['nodeType'] == 'CONNECTION')} steps), {len(edges)} edges, "
             f"{len(sizes)} functions (largest code {max(sizes)} bytes), {len(text)} bytes")
    return problems, stats


def scan_repo(root, skip):
    problems = []
    for dirpath, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "__pycache__", "out", ".claude")]
        for name in files:
            p = pathlib.Path(dirpath) / name
            if p.resolve() == skip or name in ("values.json",) or name.endswith(".local.json"):
                continue
            try:
                text = p.read_text(encoding="utf-8")
            except (UnicodeDecodeError, OSError):
                continue
            scan_text(str(p.relative_to(root)), text, problems)
    return problems


def self_test(export):
    """Break the export in ways the trust boundary forbids; each must be caught."""
    import copy
    import tempfile
    base = json.loads(pathlib.Path(export).read_text(encoding="utf-8"))

    def node(f, t):
        return [n["data"] for n in f["graphData"]["elements"]["nodes"] if n["data"].get("properties", {}).get("nodeTitle", {}).get("value") == t][0]

    def link(ref):
        return json.dumps([{"children": [{"text": ""}, {"type": "link", "data": "{{" + ref + "}}", "children": [{"text": "x"}]}, {"text": ""}]}])

    def secret_into_function(f):
        node(f, "Render screen")["properties"]["variableInputList"]["value"][0]["value"] = link("global.company.variables.ppGitlabToken")

    def gitlab_token_to_pingone(f):
        node(f, "PingOne: list flows (id, name)")["properties"]["headers"]["value"].append({"key": "PRIVATE-TOKEN", "value": link("global.company.variables.ppGitlabToken")})

    def pingone_write(f):
        node(f, "PingOne: list flows (id, name)")["properties"]["httpMethod"]["value"] = "POST"

    def preset_env(f):
        next(v for v in f["variables"] if v["name"].startswith("ppSourceEnvs##"))["fields"]["value"] = "dev=x"

    def state_in_company(f):
        v = next(v for v in f["variables"] if v["name"].startswith("ppState##"))
        v["context"], v["name"] = "company", "ppState##SK##company"

    def setting_in_flow(f):
        v = next(v for v in f["variables"] if v["name"].startswith("ppMrLabel##"))
        v["context"], v["name"], v["flowId"] = "flow", "ppMrLabel##SK##flow##SK##" + f["flowId"], f["flowId"]

    def secret_in_body(f):
        node(f, "GitLab: commit request file")["properties"]["raw"]["value"] = "{{global.company.variables.ppGitlabToken}}"

    def token_on_screen(f):
        node(f, "App screen")["properties"]["customHTML"]["value"] += "{{local.pn00000001.payload.output.rawResponse.body.access_token}}"

    def extra_gitlab_post(f):
        node(f, "GitLab: delete request branch")["properties"]["httpMethod"]["value"] = "POST"

    def fixed_url(f):
        node(f, "GitLab: read")["properties"]["url"]["value"] = json.dumps([{"children": [{"text": "https://gitlab.example.com/api/v4/projects/1/pipelines/1/play"}]}])

    def job_log_write(f):
        node(f, "GitLab: read job log (last 64 KiB)")["properties"]["httpMethod"]["value"] = "POST"

    failures = 0
    for case in (job_log_write, state_in_company, setting_in_flow, secret_into_function, gitlab_token_to_pingone, pingone_write, preset_env, secret_in_body, token_on_screen, extra_gitlab_post, fixed_url):
        data = copy.deepcopy(base)
        case(data["flows"][0])
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as tmp:
            json.dump(data, tmp)
        problems, _ = check_export(tmp.name, False)
        os.unlink(tmp.name)
        print(f"self-test {case.__name__}: {'caught' if problems else 'MISSED'}")
        failures += 0 if problems else 1
    return failures


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true", help="also check that broken copies of the export are refused")
    ap.add_argument("export", nargs="?", default=str(DEFAULT))
    ap.add_argument("--repo", default=str(HERE), help="directory to scan for secrets and IDs; defaults to this folder ('' to skip)")
    ap.add_argument("--allow-connection-ids", action="store_true", help="for a private copy generated with your connection and subflow IDs")
    a = ap.parse_args()
    problems, stats = check_export(a.export, a.allow_connection_ids)
    if a.repo:
        problems += scan_repo(pathlib.Path(a.repo), pathlib.Path(a.export).resolve())
    print(f"{pathlib.Path(a.export).name}: {stats} -> {'ok' if not problems else str(len(problems)) + ' problem(s)'}")
    for p in problems[:40]:
        print("   ", p)
    missed = self_test(a.export) if a.self_test else 0
    sys.exit(1 if problems or missed else 0)


if __name__ == "__main__":
    main()
