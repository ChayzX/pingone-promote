"""Fixtures covering every branch of Inspect, BuildRequest, Redact and the drift gate."""
import copy
import json

O = "https://api.pingone.com"


def res(env, path, rid, typ, conf, deps=None):
    return {"id": rid, "url": f"{O}/v1/environments/{env}/{path}", "name": conf.get("name", rid), "type": typ,
            "configuration": conf, "dependencies": deps or []}


SRC = {"apiOrigin": O, "environmentId": "src", "organizationId": "org", "snapshotId": "s1", "resources": [
    res("src", "flows/f1", "f1", "flows", {"name": "Login", "enabled": True, "settings": {"a": 1, "b": {"c": "x"}}}),
    res("src", "flows/f2", "f2", "flows", {"name": "Reg", "clientSecret": "sss"}),
    res("src", "applications/a1", "a1", "applications", {"name": "App", "tokenEndpointAuthMethod": "basic"}),
    res("src", "applications/a2", "a2", "applications", {"name": "App2"}),
    res("src", "applications/a1/grants/g1", "g1", "applications / grants", {"name": "G"}),
]}
TGT = {"apiOrigin": O, "environmentId": "tgt", "organizationId": "org", "snapshotId": "t1", "resources": [
    res("tgt", "flows/tf1", "tf1", "flows", {"name": "Login", "enabled": False, "settings": {"a": 1, "b": {"c": "y"}}}),
    res("tgt", "applications/ta1", "ta1", "applications", {"name": "App", "password": "p"}),
    res("tgt", "applications/ta2", "ta2", "applications", {"name": "Other"}, ["tf1"]),
]}


def step(action, url, sid="", tid="", payload="__none__"):
    s = {"action": action, "resourceUrl": url, "sourceResource": {"id": sid}, "targetResource": {"id": tid}}
    if payload != "__none__":
        s["payload"] = payload
    return s


T = f"{O}/v1/environments/tgt"
REDACT = {"name": "n", "Client_Secret": "x", "api-key": "k", "isSensitive": True, "value": "v", "nested": [{"auth": 1, "AUTHENTICATION": {"a": 1}, "ok": [1, {"refresh_token": "t"}]}], "credentials": None}

cases = []


def case(name, steps, selected=("f1", "a1"), mappings=None, rollback=False, src=None, tgt=None, live=None):
    cases.append({"name": name, "source": src or SRC, "target": tgt or TGT, "selected": list(selected),
                  "mappings": mappings if mappings is not None else {"f1": "tf1", "a1": "ta1"},
                  "steps": steps, "rollback": rollback, "redact": REDACT,
                  "live": live or {"apiOrigin": "", "environmentId": "", "organizationId": "", "snapshotId": "", "resources": []}})


ok_steps = [step("PUT", f"{T}/flows/tf1", "f1", "tf1", {"name": "Login", "enabled": True, "settings": {"a": 1, "b": {"c": "x"}}}),
            step("PUT", f"{T}/applications/ta1", "a1", "ta1", {"name": "App", "tokenEndpointAuthMethod": "basic", "secret": "z"})]
case("valid mapped updates", ok_steps)
case("empty plan", [])
case("create into collection", [step("POST", f"{T}/applications", "a2", "", {"id": "new-a2", "name": "App2"})],
     selected=["a2"], mappings={})
case("created flow child", [step("POST", f"{T}/flows", "f2", "nf2", {"name": "Reg"}),
                            step("POST", f"{T}/flows/nf2/versions", "f2", "", {"v": 1})], selected=["f2"], mappings={})
case("created flow child without root", [step("POST", f"{T}/flows/nf2/versions", "f2", "", {"v": 1})], selected=["f2"], mappings={})
case("mapped flow versions", [step("POST", f"{T}/flows/tf1/versions/3", "f1", "tf1", {"v": 3})])
case("bodyless flow deploy", [step("POST", f"{T}/flows/tf1", "f1", "tf1")])
case("null payload flow deploy", [step("POST", f"{T}/flows/tf1", "f1", "tf1", None)])
case("bodyless non-flow", [step("POST", f"{T}/applications/ta1", "a1", "ta1")])
case("array payload", [step("PUT", f"{T}/flows/tf1", "f1", "tf1", [1, 2])])
case("unselected source", [step("PUT", f"{T}/applications/ta2", "a2", "ta2", {"name": "x"})])
case("unknown source", [step("PUT", f"{T}/flows/tf1", "zz", "tf1", {"name": "x"})])
case("missing source", [step("PUT", f"{T}/flows/tf1", "", "tf1", {"name": "x"})])
case("outside environment", [step("PUT", f"{O}/v1/environments/other/flows/tf1", "f1", "tf1", {"name": "x"})])
case("other host", [step("PUT", "https://evil.example/v1/environments/tgt/flows/tf1", "f1", "tf1", {"name": "x"})])
case("http scheme", [step("PUT", "http://api.pingone.com/v1/environments/tgt/flows/tf1", "f1", "tf1", {"name": "x"})])
case("query string", [step("PUT", f"{T}/flows/tf1?x=1", "f1", "tf1", {"name": "x"})])
case("fragment", [step("PUT", f"{T}/flows/tf1#x", "f1", "tf1", {"name": "x"})])
case("userinfo", [step("PUT", "https://u:p@api.pingone.com/v1/environments/tgt/flows/tf1", "f1", "tf1", {"name": "x"})])
case("encoded path", [step("PUT", f"{T}/flows/tf%31", "f1", "tf1", {"name": "x"})])
case("dot segment", [step("PUT", f"{T}/flows/../applications/ta1", "f1", "tf1", {"name": "x"})])
case("double slash", [step("PUT", f"{T}//flows/tf1", "f1", "tf1", {"name": "x"})])
case("not a url", [step("PUT", "not a url", "f1", "tf1", {"name": "x"})])
case("delete forward", [step("DELETE", f"{T}/flows/tf1", "f1", "tf1", {"name": "x"})])
case("get action", [step("GET", f"{T}/flows/tf1", "f1", "tf1", {"name": "x"})])
case("lowercase action", [step("put", f"{T}/flows/tf1", "f1", "tf1", {"name": "x"})])
case("step target differs from mapping", [step("PUT", f"{T}/flows/tf1", "f1", "ta1", {"name": "x"})])
case("mapped path mismatch", [step("PUT", f"{T}/applications/ta1", "f1", "tf1", {"name": "x"})])
case("unmapped non-create", [step("PUT", f"{T}/applications/ta2", "a1", "ta2", {"name": "x"})], mappings={})
case("create into wrong collection", [step("POST", f"{T}/flows", "a1", "", {"name": "x"})], mappings={})
case("mapping to unknown target", [step("PUT", f"{T}/flows/tf1", "f1", "tf1", {"name": "x"})], mappings={"f1": "nope"})
case("mapping type mismatch", [step("PUT", f"{T}/flows/tf1", "f1", "tf1", {"name": "x"})], mappings={"f1": "ta1"})
case("rollback delete bodyless", [step("DELETE", f"{T}/flows/tf1", "", "tf1")], rollback=True)
case("rollback put", [step("PUT", f"{T}/flows/tf1", "", "tf1", {"name": "Login"})], rollback=True)
case("rollback unknown source ok", [step("PUT", f"{T}/flows/tf1", "zz", "tf1", {"name": "Login"})], rollback=True)
case("rollback outside env", [step("DELETE", f"{O}/v1/environments/x/flows/tf1", "", "tf1")], rollback=True)
case("nested diff and redaction", [step("PUT", f"{T}/flows/tf1", "f1", "tf1",
     {"name": "Login", "enabled": True, "settings": {"a": 2, "b": {"c": "y", "d": [1]}}, "apiKey": "k", "new": None})])
case("trailing slash mapped", [step("PUT", f"{T}/flows/tf1/", "f1", "tf1", {"name": "x"})])
# BuildRequest-only failures
case("build: duplicate selection", [], selected=["f1", "f1"])
case("build: unknown selection", [], selected=["nope"])
case("build: empty selection", [], selected=[])
case("build: multiple sources map to one target", [], selected=["a1", "a2"], mappings={"a1": "ta1", "a2": "ta1"})
other_org = copy.deepcopy(TGT)
other_org["organizationId"] = "org2"
case("build: different organizations", [], tgt=other_org)
same_env = copy.deepcopy(TGT)
same_env["environmentId"] = "src"
case("build: same environment", [], tgt=same_env)
unpinned = copy.deepcopy(SRC)
unpinned["snapshotId"] = ""
case("build: unpinned source", [], src=unpinned)

# Drift gate
LIVE = copy.deepcopy(TGT)
LIVE["snapshotId"] = "t2"
case("drift: unchanged", ok_steps, live=LIVE)
changed = copy.deepcopy(LIVE)
changed["resources"][0]["configuration"]["enabled"] = True
case("drift: affected changed", ok_steps, live=changed)
removed = copy.deepcopy(LIVE)
removed["resources"] = removed["resources"][1:]
case("drift: affected removed", ok_steps, live=removed)
unaffected = copy.deepcopy(LIVE)
unaffected["resources"][2]["configuration"]["name"] = "Edited"
case("drift: unaffected change", ok_steps, live=unaffected)
deps = copy.deepcopy(LIVE)
deps["resources"][1]["dependencies"] = ["tf1"]
case("drift: dependency changed", ok_steps, live=deps)
retyped = copy.deepcopy(LIVE)
retyped["resources"][1]["type"] = "applications / x"
case("drift: type changed", ok_steps, live=retyped)
create = [step("POST", f"{T}/applications", "a2", "", {"name": "App2"})]
member = copy.deepcopy(LIVE)
member["resources"].append(res("tgt", "applications/ta3", "ta3", "applications", {"name": "App2"}))
case("drift: concurrent create into collection", create, selected=["a2"], mappings={}, live=member)
case("drift: create, collection unchanged", create, selected=["a2"], mappings={}, live=LIVE)
appeared = copy.deepcopy(LIVE)
appeared["resources"].append(res("tgt", "flows/nf2", "nf2", "flows", {"name": "Reg"}))
case("drift: step target appeared", [step("PUT", f"{T}/flows/nf2", "f2", "nf2", {"name": "Reg"})], selected=["f2"], mappings={}, live=appeared)
other = copy.deepcopy(LIVE)
other["organizationId"] = "org2"
case("drift: identity differs", ok_steps, live=other)
case("drift: unresolvable step URL", [step("PUT", f"{T}/flows/tf1?x=1", "f1", "tf1", {"name": "x"})], live=LIVE)
child = copy.deepcopy(LIVE)
child["resources"][0]["configuration"]["name"] = "Changed"
case("drift: child URL of affected resource", [step("POST", f"{T}/flows/tf1/versions", "f1", "", {"v": 1})], mappings={}, live=child)

print(json.dumps(cases))
