"""Snapshot-response fixtures for the inventory cross-check (ORIGIN = API base)."""
import copy
import json

ENV = {"id": "src", "organization": {"id": "org"}}
SNAP = {"snapshotId": "pin", "resource": {"id": "src"}, "status": "COMPLETE"}


def item(rid, path, conf, extra=None):
    d = {"resource": {"id": rid}, "resourceUrl": "ORIGIN/v1/environments/src/" + path, "configuration": conf,
         "referencedResources": ["dep1"]}
    d.update(extra or {})
    return d


def page(items, count=None, next_page=None):
    p = {"_embedded": {"referencedResources": items}}
    if count is not None:
        p["count"] = count
    if next_page is not None:
        p["_links"] = {"next": {"href": "?page=%d" % next_page}}
    return p


GOOD = [page([item("f1", "flows/f1", {"name": "Login"}), item("g1", "applications/a1/grants/g1", {"displayName": "Grant"})], 3, 1),
        page([item("a1", "applications/a1", {})], 3)]

cases = []


def case(name, pages, pin="", env=None, snap=None):
    for p in [pin] if pin else ["", "pin"]:
        cases.append({"name": name + (" (pinned)" if p else " (live)"), "env": "src", "pin": p, "envRecord": env or ENV,
                      "snapshot": snap or SNAP, "pages": pages})


case("two pages, names and nested types", GOOD)
case("single page no count", [page([item("f1", "flows/f1", {"name": "x"})])])
case("incomplete pagination", [page([item("f1", "flows/f1", {"name": "x"})], 2)])
case("missing collection", [{"_embedded": {}}])
case("deleted configuration", [page([item("f1", "flows/f1", None)])])
case("incomplete resource", [page([item("f1", "flows/f1", {"name": "x"}, {"status": "PREPARING"})])])
case("duplicate id", [page([item("f1", "flows/f1", {"name": "x"}), item("f1", "flows/f2", {"name": "y"})])])
case("other environment url", [page([item("f1", "../other/flows/f1", {"name": "x"})])])
case("query in url", [page([item("f1", "flows/f1?x=1", {"name": "x"})])])
case("empty resource id", [page([item("", "flows/f1", {"name": "x"})])])
case("url not ending in id", [page([item("f1", "flows/other", {"name": "x"})])])
case("unsafe id", [page([item("f 1", "flows/f 1", {"name": "x"})])])
case("environment without org", GOOD, env={"id": "src"})
case("environment id mismatch", GOOD, env={"id": "other", "organization": {"id": "org"}})
case("snapshot for other env", GOOD, snap={"snapshotId": "pin", "resource": {"id": "x"}, "status": "COMPLETE"})
case("snapshot errored", GOOD, snap={"snapshotId": "pin", "resource": {"id": "src"}, "status": "ERROR"})
case("pinned id mismatch", GOOD, pin="pin", snap={"snapshotId": "other", "resource": {"id": "src"}, "status": "COMPLETE"})
print(json.dumps(cases))
