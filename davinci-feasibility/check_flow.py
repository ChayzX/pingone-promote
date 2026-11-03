"""Validate the feasibility flow export and scan the repository for secrets and IDs.

usage: python3 check_flow.py [EXPORT.json] [--repo DIR] [--allow-connection-ids]

Structure (as the prior iteration's check_flows.py, plus flow-instance state):
- JSON parses; one flow in the {"flows": [...]} wrapper with the export fields
- unique 10-character node IDs; every edge endpoint exists; one start node;
  every node reachable; every evaluator false branch is a real edge
- every REST call has a failure branch and the live-confirmed property shape
- every {{local.<node>...}} reference names a node that can run before it, and
  a function output it reads is declared in that function's output schema
- every flow-instance variable read is declared and written by an earlier
  Variables node; every flow and company variable read is declared
- only screens end a path; custom functions use the DaVinci wrapper and read
  exactly the inputs wired to them; every routed action is one the code returns
- no <style>/<script> inside Custom HTML (DaVinci ignores them there)
Hygiene (export and every text file in the repo):
- no UUIDs except the all-zero placeholder, no 32-hex IDs except the known
  placeholders, no bearer/basic credentials, GitLab tokens, JWTs or private keys
- ID and secret settings are empty in the export
"""
import argparse
import collections
import json
import os
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
DEFAULT = HERE / "flow" / "pingone-promote-feasibility.flow.json"
ZERO_UUID = "00000000-0000-0000-0000-000000000000"
ALLOWED_HEX = {"0" * 32, "f0d00a7e00000000000000000000fe01", "f0d00a7e00000000000000000000fe10", "f0d00a7e00000000000000000000fe11"} | {f"a1f0d00a00000000000000000000000{i}" for i in range(10)}
REST_KEYS = {"url", "httpMethod", "headers", "httpBody", "raw", "nodeTitle"}
SCREEN = "customHTMLTemplate"
EMPTY_SETTINGS = {"fsWorkerEnvironmentId", "fsSourceEnvironmentId", "fsGitlabProjectId"}
SCANS = [
    ("UUID", re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")),
    ("32-hex ID", re.compile(r"(?<![0-9a-fA-F])[0-9a-fA-F]{32}(?![0-9a-fA-F])")),
    ("bearer/basic credential", re.compile(r"\b(?:Bearer|Basic)\s+(?!\{\{|<|\$|\"|'|\+|\(|\[)[A-Za-z0-9._~+/=-]{16,}")),
    ("GitLab token", re.compile(r"glpat-[A-Za-z0-9_-]{16,}")),
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
    if "trigger" in f and not (isinstance(f["trigger"], dict) and isinstance(f["trigger"].get("type"), str)):
        problems.append("trigger must be {type: ...}")
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
    if len(starts) != 1:
        problems.append(f"expected one start node, found {starts}")
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
    for name, v in declared["company"].items():
        if v["fields"].get("type") != "secret" or v["fields"].get("value") not in ("", None):
            problems.append(f"company variable {name} must be an empty secret")
    for name in EMPTY_SETTINGS:
        if declared["flow"].get(name, {}).get("fields", {}).get("value", "") != "":
            problems.append(f"{name} must be empty in the export (set it in DaVinci)")
    written = collections.defaultdict(set)
    outputs = {}
    for i, n in nodes.items():
        cap = n.get("capabilityName")
        p = n.get("properties", {})
        if cap == "saveValue":
            for item in p["saveVariables"]["value"]:
                written[item["name"]].add(i)
                if item["name"] not in declared["flowInstance"]:
                    problems.append(f"{i} saves undeclared flow-instance variable {item['name']}")
        if cap == "customFunction":
            outputs[i] = set(json.loads(p["outputSchema"]["value"])["properties"])
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
        for ctx, rx in (("flow", r"global\.flow\.variables\.([A-Za-z0-9_]+)"), ("company", r"global\.company\.variables\.([A-Za-z0-9_]+)")):
            for var in set(re.findall(rx, blob)):
                if var not in declared[ctx]:
                    problems.append(f"{i} reads undeclared {ctx} variable {var}")
        if n["nodeType"] == "EVAL":
            if not succ[i] or len(pred[i]) != 1:
                problems.append(f"evaluator {i} must have one source and at least one target")
            for t in p or {}:
                if t not in succ[i]:
                    problems.append(f"evaluator {i} false branch {t} is not an edge")
            continue
        if not succ[i] and cap != SCREEN:
            problems.append(f"non-terminal dead end {i} ({title(i)})")
        if cap == SCREEN and re.search(r"<(style|script)\b", p["customHTML"]["value"], re.I):
            problems.append(f"{i} ({title(i)}) has <style>/<script> in its HTML; use customCSS/customScript")
        if cap == "makeRestApiCall":
            if set(p) - REST_KEYS:
                problems.append(f"{i} REST node has unknown properties {sorted(set(p) - REST_KEYS)}")
            if p.get("httpMethod", {}).get("value") not in ("GET", "POST"):
                problems.append(f"{i} REST node must be GET or POST (PingOne is read-only; GitLab writes are POSTs)")
            if ("raw" in p) != (p.get("httpBody", {}).get("value") == "raw"):
                problems.append(f"{i} REST body: httpBody must be 'raw' exactly when raw is set")
            if "raw" in p and not isinstance(p["raw"]["value"], str):
                problems.append(f"{i} raw body must be plain text")
            evs = [e for e in succ[i] if nodes[e]["nodeType"] == "EVAL"]
            if len(evs) != 1 or not any(v.get("value") == "allTriggersFalse" for v in (nodes[evs[0]].get("properties") or {}).values()):
                problems.append(f"{i} ({title(i)}) REST call has no failure branch")
            url = json.dumps(p["url"]["value"])
            if "global.variables.fsUrl" not in url:
                problems.append(f"{i} ({title(i)}) URL must come from fsUrl (built and checked by a function)")
            hdrs = json.dumps(p["headers"]["value"])
            if "Bearer" in hdrs and "rawResponse.body.access_token" not in hdrs:
                problems.append(f"{i} bearer header must reference the token node")
            is_gitlab = title(i).startswith("5 GitLab")
            is_token = "/as/token" in json.dumps(p) or "fsWorkerBasicAuth" in hdrs
            if not is_gitlab and not is_token and p["httpMethod"]["value"] != "GET":
                problems.append(f"{i} ({title(i)}) PingOne calls must be read-only (GET)")
            if is_gitlab and "fsGitlabToken" not in hdrs:
                problems.append(f"{i} GitLab call without the GitLab token header")
            if not is_gitlab and "fsGitlabToken" in hdrs:
                problems.append(f"{i} GitLab token sent to a non-GitLab call")
        if cap == "customFunction":
            code = p["code"]["value"]
            if not code.startswith("module.exports = a = async ({params}) => {\nconst lib = (() => {") or "'use strict'" in code:
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
                code = nodes[m.group(1)]["properties"]["code"]["value"].split("// ---- step: ", 1)[-1]
                if f"'{value}'" not in code:
                    problems.append(f"{i} routes on action '{value}' which {title(m.group(1))} never returns")
    # Hygiene of the export as shipped.
    scanned = text
    if allow_conn_ids:
        scanned = re.sub(r'"connectionId": "[0-9a-f]{32}"', '"connectionId": "x"', scanned)
    scan_text(pathlib.Path(path).name, scanned, problems)
    sizes = [len(n["properties"]["code"]["value"]) for n in nodes.values() if n.get("capabilityName") == "customFunction"]
    stats = (f"{len(nodes)} nodes ({sum(1 for n in nodes.values() if n['nodeType'] == 'CONNECTION')} steps), {len(edges)} edges, "
             f"{len(sizes)} functions (largest code {max(sizes)} bytes), {len(text)} bytes")
    return problems, stats


def scan_repo(root, skip):
    problems = []
    for dirpath, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "__pycache__", "out")]
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("export", nargs="?", default=str(DEFAULT))
    ap.add_argument("--repo", default=str(HERE), help="directory to scan for secrets and IDs; defaults to this folder ('' to skip)")
    ap.add_argument("--allow-connection-ids", action="store_true", help="for a private copy generated with your connection IDs")
    a = ap.parse_args()
    problems, stats = check_export(a.export, a.allow_connection_ids)
    if a.repo:
        problems += scan_repo(pathlib.Path(a.repo), pathlib.Path(a.export).resolve())
    print(f"{pathlib.Path(a.export).name}: {stats} -> {'ok' if not problems else str(len(problems)) + ' problem(s)'}")
    for p in problems[:40]:
        print("   ", p)
    sys.exit(1 if problems else 0)


if __name__ == "__main__":
    main()
