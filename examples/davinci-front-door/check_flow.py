"""Structural checks for the generated DaVinci flow export."""
import json
import re
import sys

f = json.load(open(sys.argv[1]))
els = f["graphData"]["elements"]
nodes = {n["data"]["id"]: n["data"] for n in els["nodes"]}
edges = [e["data"] for e in els["edges"]]
assert len(nodes) == len(els["nodes"]), "duplicate node IDs"
assert all(re.fullmatch(r"[a-z0-9]{10}", i) for i in nodes), "node IDs must be 10 lowercase alphanumerics"
for e in edges:
    assert e["source"] in nodes and e["target"] in nodes, e
for i, n in nodes.items():
    if n["nodeType"] == "EVAL":
        for t in n.get("properties") or {}:
            assert any(e["source"] == i and e["target"] == t for e in edges), (i, t)
targets = {e["target"] for e in edges}
starts = [i for i in nodes if i not in targets]
assert len(starts) == 1, starts
order, seen = [], set()


def walk(i):
    if i in seen:
        return
    seen.add(i)
    order.append(i)
    for e in edges:
        if e["source"] == i:
            walk(e["target"])


walk(starts[0])
assert len(order) == len(nodes), "unreachable nodes"
pos = {i: k for k, i in enumerate(order)}
for i, n in nodes.items():
    for ref in set(re.findall(r"local\.([a-z0-9]{10})\.payload", json.dumps(n.get("properties", {})))):
        assert ref in nodes and pos[ref] < pos[i], f"{i} references {ref} out of order"
    if n.get("capabilityName") == "customFunction":
        code = n["properties"]["code"]["value"]
        assert code.startswith("module.exports = a = async ({params}) =>") and "use strict" not in code
names = {v["name"].split("##SK##")[0] for v in f["variables"]}
for ref in set(re.findall(r"global\.(?:flow|company)\.variables\.([A-Za-z0-9]+)", json.dumps(f["graphData"]))):
    assert ref in names, f"undeclared variable {ref}"
print(f"ok: {len(nodes)} nodes, {len(edges)} edges, start {starts[0]}, all references ordered and declared")
