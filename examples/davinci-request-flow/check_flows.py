"""Structural checks for generated DaVinci exports (loops allowed).

- unique 10-character node IDs; every edge endpoint exists
- exactly one start node; every node reachable from it
- every evaluator's false branch is a real outgoing edge
- every {{local.<node>...}} reference names a node that can run before it
- every flow variable read is a setting or is written by a reachable earlier
  Variables node; every company variable read is declared
- only terminal capabilities (error/message/result page) have no successor
- no unresolved alias placeholders; custom functions use the DaVinci wrapper
- multi-flow files (main flow + subflows): node IDs unique across the file;
  each Invoke UI Subflow node names a subflow in the file and passes exactly
  its input schema; {{global.parameters.x}} only for declared inputs; the
  parent reads only values the subflow returns; and every path through a
  subflow shows at least one screen before it returns (Ping's rule for UI subflows)
"""
import collections
import json
import re
import sys

TERMINAL = {"customErrorMessage", "customHtmlMessage", "createSuccessResponse"}
SUBFLOW_META = {"nodeTitle", "subFlowId", "subFlowVersionId"}


def subflow_checks(f, by_id, nodes, succ, starts):
    problems = []
    inputs = {s["propertyName"] for s in f.get("inputSchema") or []}
    blob = json.dumps(f["graphData"])
    for p in sorted(set(re.findall(r"global\.parameters\.([A-Za-z0-9_]+)", blob)) - inputs):
        problems.append(f"reads {{{{global.parameters.{p}}}}} but its input schema has no {p}")
    for i, n in nodes.items():
        if n.get("capabilityName") != "startUiSubFlow":
            continue
        sid = n["properties"]["subFlowId"]["value"]["value"]
        sub = by_id.get(sid)
        if not sub:
            problems.append(f"{i} invokes subflow {sid} which is not in the file")
            continue
        want = {s["propertyName"] for s in sub.get("inputSchema") or []}
        given = set(n["properties"]) - SUBFLOW_META
        if want != given:
            problems.append(f"{i} ({n['properties']['nodeTitle']['value']}) passes {sorted(given)} but {sub['name']} expects {sorted(want)}")
        returned = set(((sub.get("outputSchemaCompiled") or {}).get("output") or {}).get("properties") or {})
        for k in set(re.findall(r"local\.%s\.payload\.output\.([A-Za-z0-9_]+)" % i, blob)) - returned:
            problems.append(f"reads {k} from {i}, but {sub['name']} does not return it")
    if inputs:
        # Invoke UI Subflow: every path from the start to a success response passes a screen.
        seen, q = set(), [s for s in starts if nodes[s].get("capabilityName") != "customHTMLTemplate"]
        while q:
            x = q.pop()
            if x in seen:
                continue
            seen.add(x)
            if nodes[x].get("capabilityName") == "createSuccessResponse":
                problems.append(f"a path reaches {x} (success response) without showing a screen")
                break
            q.extend(y for y in succ[x] if nodes[y].get("capabilityName") != "customHTMLTemplate")
        claims = {c["name"] for n in nodes.values() if n.get("capabilityName") == "createSuccessResponse"
                  for c in n["properties"]["claimsNameValuePairs"]["value"]}
        declared = set(((f.get("outputSchemaCompiled") or {}).get("output") or {}).get("properties") or {})
        if claims != declared:
            problems.append(f"returns {sorted(claims)} but its output schema declares {sorted(declared)}")
    return problems


errors = 0
for path in sys.argv[1:]:
  data = json.load(open(path))
  file_flows = data["flows"] if "flows" in data else [data]
  by_id = {fl["flowId"]: fl for fl in file_flows}
  all_ids = [n["data"]["id"] for fl in file_flows for n in fl["graphData"]["elements"]["nodes"]]
  if len(all_ids) != len(set(all_ids)):
      print(f"{path}: node IDs repeat across flows")
      errors += 1
  for f in file_flows:
      els = f["graphData"]["elements"]
      nodes = {n["data"]["id"]: n["data"] for n in els["nodes"]}
      edges = [e["data"] for e in els["edges"]]
      problems = []
      if len(nodes) != len(els["nodes"]):
          problems.append("duplicate node IDs")
      for i in nodes:
          if not re.fullmatch(r"[a-z0-9]{10}", i):
              problems.append(f"bad node id {i}")
      # Make REST API Call: the property names a live DaVinci export uses.
      rest_keys = {"url", "httpMethod", "headers", "httpBody", "raw", "nodeTitle"}
      for i, n in nodes.items():
          if n.get("capabilityName") != "makeRestApiCall":
              continue
          p = n.get("properties", {})
          if set(p) - rest_keys:
              problems.append(f"{i} REST node has unknown properties {sorted(set(p) - rest_keys)}")
          if p.get("httpMethod", {}).get("value") not in ("GET", "POST", "PUT", "PATCH", "DELETE"):
              problems.append(f"{i} REST node has no valid httpMethod")
          if ("raw" in p) != (p.get("httpBody", {}).get("value") == "raw"):
              problems.append(f"{i} REST node body: httpBody must be 'raw' exactly when raw is set")
          if "raw" in p and (not isinstance(p["raw"].get("value"), str) or p["raw"]["value"].lstrip().startswith('[\n  {\n    "children"')):
              problems.append(f"{i} REST node raw body must be plain text, not rich text")
      succ, pred = collections.defaultdict(set), collections.defaultdict(set)
      for e in edges:
          if e["source"] not in nodes or e["target"] not in nodes:
              problems.append(f"dangling edge {e}")
              continue
          succ[e["source"]].add(e["target"])
          pred[e["target"]].add(e["source"])
      starts = [i for i in nodes if not pred[i]]
      if len(starts) != 1:
          problems.append(f"expected one start node, found {starts}")
      seen, queue = set(), list(starts[:1])
      while queue:
          i = queue.pop()
          if i not in seen:
              seen.add(i)
              queue.extend(succ[i])
      if seen != set(nodes):
          problems.append(f"unreachable nodes: {sorted(set(nodes) - seen)[:5]}")
      # DaVinci ignores <style>/<script> inside Custom HTML: they belong in the node's CSS and Script fields.
      for i, n in nodes.items():
          if n.get("capabilityName") == "customHTMLTemplate" and re.search(r"<(style|script)\b", n["properties"]["customHTML"]["value"], re.I):
              problems.append(f"{i} ({n['properties']['nodeTitle']['value']}) has <style>/<script> in its HTML; use customCSS/customScript")
      # A failed REST call must lead somewhere: otherwise DaVinci shows its generic error page.
      for i, n in nodes.items():
          if n.get("capabilityName") == "makeRestApiCall":
              evs = [e for e in succ[i] if nodes[e]["nodeType"] == "EVAL"]
              if len(evs) != 1 or not any(v.get("value") == "allTriggersFalse" for v in (nodes[evs[0]].get("properties") or {}).values()):
                  problems.append(f"{i} ({n['properties']['nodeTitle']['value']}) REST call has no failure branch")

      def ancestors(i):
          out, q = set(), list(pred[i])
          while q:
              x = q.pop()
              if x not in out:
                  out.add(x)
                  q.extend(pred[x])
          return out

      settings = {v["name"].split("##SK##")[0] for v in f["variables"] if v["context"] == "flow" and v["fields"].get("value") not in (None,)}
      written_by = collections.defaultdict(set)
      for i, n in nodes.items():
          if n.get("capabilityName") == "saveFlowValue":
              for item in n["properties"]["saveFlowVariables"]["value"]:
                  written_by[item["name"]].add(i)
      declared_company = {v["name"].split("##SK##")[0] for v in f["variables"] if v["context"] == "company"}
      # Settings come with the import as flow variables the admin edits in DaVinci.
      preset = {"apiOrigin", "authOrigin", "adminEnvironmentId", "hostEnvironmentId",
                "env1Name", "env1Id", "env2Name", "env2Id", "env3Name", "env3Id", "env4Name", "env4Id", "organizationId",
                "env1GitlabEnvironment", "env2GitlabEnvironment", "env3GitlabEnvironment", "env4GitlabEnvironment", "davinciAdminRoleId", "sourceRegion", "targetRegion", "gitlabUrl", "gitlabProjectId",
                "gitlabEnvironment"}
      if f.get("inputSchema"):
          preset = set()  # a subflow has no admin settings; it gets inputs instead
      for i, n in nodes.items():
          blob = json.dumps(n.get("properties", {}))
          anc = ancestors(i)
          for ref in set(re.findall(r"local\.([A-Za-z0-9]+)\.payload", blob)):
              if ref not in nodes:
                  problems.append(f"{i} references missing node {ref}")
              elif ref not in anc:
                  problems.append(f"{i} ({n.get('properties', {}).get('nodeTitle', {}).get('value')}) references {ref} which cannot run before it")
          for var in set(re.findall(r"global\.flow\.variables\.([A-Za-z0-9]+)", blob)):
              if var in preset:
                  continue
              if not (written_by[var] & (anc | {i})):
                  problems.append(f"{i} ({n.get('properties', {}).get('nodeTitle', {}).get('value')}) reads flow variable {var} never written before it")
          for var in set(re.findall(r"global\.company\.variables\.([A-Za-z0-9]+)", blob)):
              if var not in declared_company:
                  problems.append(f"{i} reads undeclared company variable {var}")
          if n["nodeType"] == "EVAL":
              for t in (n.get("properties") or {}):
                  if t not in succ[i]:
                      problems.append(f"evaluator {i} false branch {t} is not an edge")
          elif not succ[i] and n.get("capabilityName") not in TERMINAL and n.get("capabilityName") != "customHTMLTemplate":
              problems.append(f"non-terminal dead end {i} ({n.get('capabilityName')})")
          if n.get("capabilityName") == "customFunction":
              code = n["properties"]["code"]["value"]
              if not code.startswith("module.exports = a = async ({params}) => {\nconst core = (() => {") or "'use strict'" in code:
                  problems.append(f"{i} function wrapper wrong")
              step = code.split("// ---- step: ", 1)[-1]
              read = set(re.findall(r"params\.([A-Za-z0-9_]+)", step))
              given = {x["name"] for x in n["properties"]["variableInputList"]["value"]}
              if read - given:
                  problems.append(f"{i} ({n['properties']['nodeTitle']['value']}) code reads params not wired: {sorted(read - given)}")
              if given - read:
                  problems.append(f"{i} ({n['properties']['nodeTitle']['value']}) wires unused inputs: {sorted(given - read)}")
      for alias in ("REVIEW", "INVPIN", "INVLIVE", "CREATE", "BUILD", "SELECT", "AUTHZ", "EXEC", "FORM", "CONFIRM", "ROLLBACK", "GETORIG", "PAGE"):
          if f"local.{alias}." in json.dumps(f):
              problems.append(f"unresolved alias {alias}")
      problems += subflow_checks(f, by_id, nodes, succ, starts)
      status = "ok" if not problems else f"{len(problems)} problem(s)"
      print(f"{path.split('/')[-1]} [{f['name']}]: {len(nodes)} nodes, {len(edges)} edges, start {starts[:1]} -> {status}")
      for p in problems[:25]:
          print("   ", p)
      errors += len(problems)
sys.exit(1 if errors else 0)
