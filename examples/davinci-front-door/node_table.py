"""Print a Markdown reference of every step in the flow export (for hand checks)."""
import json
import sys

f = json.load(open(sys.argv[1]))


def text(v):
    try:
        blocks = json.loads(v)
    except (TypeError, ValueError):
        return str(v)
    out = []
    for b in blocks:
        for c in b.get("children", []):
            out.append(c["data"] if c.get("type") == "link" else c.get("text", ""))
    return "".join(out)


rows = []
for n in f["graphData"]["elements"]["nodes"]:
    d = n["data"]
    if d["nodeType"] != "CONNECTION":
        continue
    p = d.get("properties", {})
    title = p.get("nodeTitle", {}).get("value", "")
    detail = ""
    if d["capabilityName"] == "makeRestApiCall":
        hdrs = "; ".join(f"{h['key']}: {text(h['value'])}" for h in p["headers"]["value"])
        detail = f"`{p['httpMethod']['value']} {text(p['url']['value'])}`<br>Headers: `{hdrs}`"
        if "rawBody" in p:
            detail += f"<br>Raw body: `{text(p['rawBody']['value'])}`"
    elif d["capabilityName"] == "customFunction":
        ins = ", ".join(i["name"] for i in p["variableInputList"]["value"])
        detail = f"Inputs: {ins}"
    elif d["capabilityName"] == "AEqualsB":
        detail = f"`{text(p['leftValueA']['value'])}` equals `{text(p['rightValueB']['value'])}`"
    elif d["capabilityName"] in ("customErrorMessage", "customHtmlMessage"):
        key = "errorMessage" if "errorMessage" in p else "message"
        detail = text(p[key]["value"])
    elif d["capabilityName"] == "customHTMLTemplate":
        detail = "Fields: " + ", ".join(x["propertyName"] for x in p["formFieldsList"]["value"])
    rows.append(f"| `{d['id']}` | {title} | {d['name']} · `{d['capabilityName']}` | {detail.replace('|', '&#124;')} |")
print("| Node | Step | Connector · capability | Configuration |\n| --- | --- | --- | --- |")
print("\n".join(rows))
