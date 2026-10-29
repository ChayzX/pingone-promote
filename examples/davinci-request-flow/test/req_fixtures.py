"""Request-building fixtures with configurations that stress Go's JSON encoding."""
import copy
import json

O = "https://api.pingone.com"


def res(env, path, rid, typ, conf):
    return {"id": rid, "url": f"{O}/v1/environments/{env}/{path}", "name": rid, "type": typ, "configuration": conf, "dependencies": []}


TRICKY = {
    "name": "Login <b>&</b> \"quoted\" \\ back",
    "html": "<script>alert('x')</script> & more",
    "lineSep": "a b c",
    "controls": "tab\tnl\ncr\rbs\bff\fbell\u0007nul\u0000del\u007f",
    "unicode": "héllo ✓ 中文 𝄞 emoji 😀",
    "numbers": [0, 1, -1, 0.1, 1.5, 1e20, 1e21, 1.5e21, 1e-6, 1e-7, 1.23e-10, 123456789012345, 9007199254740993, -0.0, 3.0, 2.5e-5],
    "nested": {"z": 1, "a": [{"y": True, "b": None}], "M": {}, "": "empty key", "é": 1, "zé": 2, "\U0001d11e": 3, "～": 4},
    "clientSecret": "s3cr3t", "api_key": "k", "accessToken": "t", "Authentication": {"x": 1}, "auth": "basic",
    "settings": [{"isSensitive": True, "value": "hidden"}, {"isSensitive": False, "value": "shown"}],
    "emptyList": [], "emptyMap": {}, "bools": [True, False], "nul": None,
}

SRC = {"apiOrigin": O, "environmentId": "src", "organizationId": "org", "snapshotId": "s1", "resources": [
    res("src", "flows/f1", "f1", "flows", {"name": "Login", "v": 2}),
    res("src", "applications/a1", "a1", "applications", TRICKY),
    res("src", "applications/a2", "a2", "applications", {"name": "Plain"}),
    res("src", "applications/a3", "a3", "applications", {"clientSecret": "only-secret"}),
    res("src", "applications/a4", "a4", "applications", {}),
]}
TGT = {"apiOrigin": O, "environmentId": "tgt", "organizationId": "org", "snapshotId": "t1", "resources": [
    res("tgt", "flows/tf1", "tf1", "flows", {"name": "Login"}),
    res("tgt", "applications/ta1", "ta1", "applications", {"name": "x"}),
]}

ID = "0123456789abcdef0123456789abcdef"
TS = "2026-10-29T06:00:00.123456789Z"
cases = []


def case(name, selected, mappings=None, description="Promote login", src=None, tgt=None, sr="us", tr="us", rid=ID, ts=TS):
    cases.append({"name": name, "source": src or SRC, "target": tgt or TGT, "sourceRegion": sr, "targetRegion": tr,
                  "selected": selected, "mappings": mappings if mappings is not None else {}, "description": description,
                  "id": rid, "createdAt": ts})


case("tricky configuration", ["a1"])
case("several with mappings, unsorted selection", ["a2", "f1", "a1"], {"f1": "tf1", "a1": "ta1"})
case("only-secret configuration", ["a3"])
case("html and unicode description", ["a2"], description="Fix <login> &   \"MFA\" ✓ 𝄞")
case("empty description", ["a2"], description="")
case("eu regions", ["a2"], sr="eu", tr="eu")
case("plain ms timestamp", ["a2"], ts="2026-10-29T06:00:00.123Z")
case("empty configuration rejected", ["a4"])
case("unknown selection rejected", ["zz"])
case("duplicate selection rejected", ["a2", "a2"])
case("bad region rejected", ["a2"], sr="moon")
case("long description rejected", ["a2"], description="x" * 4001)
case("4000-byte multibyte description", ["a2"], description="é" * 2000)
case("4001-byte multibyte description rejected", ["a2"], description="é" * 2000 + "x")
case("mapping type mismatch rejected", ["a1"], {"a1": "tf1"})
case("conflicting mappings rejected", ["a1", "a2"], {"a1": "ta1", "a2": "ta1"})
other = copy.deepcopy(TGT)
other["organizationId"] = "org2"
case("different organization rejected", ["a2"], tgt=other)
unpinned = copy.deepcopy(SRC)
unpinned["snapshotId"] = ""
case("unpinned source rejected", ["a2"], src=unpinned)
case("bad id rejected", ["a2"], rid="XYZ")
print(json.dumps(cases))
