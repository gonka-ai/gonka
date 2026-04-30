#!/usr/bin/env python3
# v0.2.12 pre-upgrade: trim each MLNode config to the enforced model. See README.md.
# Default commits. Pass any other arg (e.g. "dry") to preview only.
#   python3 cleanup_models.py          # commit
#   python3 cleanup_models.py dry      # preview
import json
import sys
import urllib.request

ADMIN = "http://127.0.0.1:9200"
KEEP = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
APPLY = (sys.argv[1] if len(sys.argv) > 1 else "--apply") == "--apply"


def req(method, path, body=None):
    r = urllib.request.Request(
        ADMIN + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(r, timeout=15).read())


for n in req("GET", "/admin/v1/config")["nodes"]:
    models = n.get("models") or {}
    if KEEP not in models:
        print(f"{n['id']}: skip ({sorted(models)})")
    elif set(models) == {KEEP}:
        print(f"{n['id']}: ok")
    else:
        print(f"{n['id']}: {sorted(models)} -> [{KEEP}]")
        if APPLY:
            n["models"] = {KEEP: models[KEEP]}
            req("PUT", f"/admin/v1/nodes/{n['id']}", n)

print("done; auto-flush persists to SQLite within 60s" if APPLY else "preview only")
