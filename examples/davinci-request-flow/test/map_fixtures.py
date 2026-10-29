"""Fixtures for the mapping-suggestion cross-check (core.suggestMappings vs Go suggestMappings)."""
import json
import random

def r(i, t, n):
    return {"id": i, "type": t, "name": n}

fx = [
    {"name": "unique name match", "source": [r("s1", "flows", "Login")], "target": [r("t1", "flows", "Login")], "native": {}},
    {"name": "case and spaces", "source": [r("s1", "flows", "  login ")], "target": [r("t1", "flows", "LOGIN")], "native": {}},
    {"name": "type must match", "source": [r("s1", "flows", "Login")], "target": [r("t1", "forms", "Login")], "native": {}},
    {"name": "two candidates: no suggestion", "source": [r("s1", "flows", "Login")],
     "target": [r("t1", "flows", "Login"), r("t2", "flows", "login")], "native": {}},
    {"name": "native first", "source": [r("s1", "flows", "Login")], "target": [r("t1", "flows", "Login"), r("t9", "flows", "Other")],
     "native": {"s1": "t9"}},
    {"name": "native wrong type ignored", "source": [r("s1", "flows", "Login")], "target": [r("t1", "flows", "Login"), r("t9", "forms", "X")],
     "native": {"s1": "t9"}},
    {"name": "native to missing target", "source": [r("s1", "flows", "Login")], "target": [r("t1", "flows", "Login")], "native": {"s1": "gone"}},
    {"name": "destination used once (native)", "source": [r("s1", "flows", "A"), r("s2", "flows", "B")], "target": [r("t1", "flows", "Z")],
     "native": {"s1": "t1", "s2": "t1"}},
    {"name": "destination used once (name after native)", "source": [r("s1", "flows", "Login"), r("s2", "flows", "Other")],
     "target": [r("t1", "flows", "Login")], "native": {"s2": "t1"}},
    {"name": "duplicate source names", "source": [r("s1", "flows", "Login"), r("s2", "flows", "Login")], "target": [r("t1", "flows", "Login")], "native": {}},
    {"name": "unicode case", "source": [r("s1", "flows", "ÉCOLE")], "target": [r("t1", "flows", "école")], "native": {}},
    {"name": "empty", "source": [], "target": [], "native": {}},
]
rnd = random.Random(7)
names = ["Login", "login", "Sign On", "MFA", "mfa ", "Reg", "Admin", "Portal"]
types = ["flows", "forms", "applications"]
for k in range(40):
    src = [r(f"s{i}", rnd.choice(types), rnd.choice(names)) for i in range(rnd.randint(0, 8))]
    tgt = [r(f"t{i}", rnd.choice(types), rnd.choice(names)) for i in range(rnd.randint(0, 8))]
    native = {s["id"]: rnd.choice([t["id"] for t in tgt] + ["missing"]) for s in src if tgt and rnd.random() < 0.4}
    fx.append({"name": f"random {k}", "source": src, "target": tgt, "native": native})
print(json.dumps(fx))
