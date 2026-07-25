#!/usr/bin/env python3
"""Filter chain /versions JSON to a allow-list of version names.

Env:
  ORACLE_UPSTREAM  default http://api:9100/versions
  ORACLE_ALLOW     comma-separated names, e.g. v3 or v4
  ORACLE_LISTEN    default 0.0.0.0:9100
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UPSTREAM = os.environ.get("ORACLE_UPSTREAM", "http://api:9100/versions")
ALLOW = {x.strip() for x in os.environ.get("ORACLE_ALLOW", "").split(",") if x.strip()}
LISTEN = os.environ.get("ORACLE_LISTEN", "0.0.0.0:9100")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802
        try:
            with urllib.request.urlopen(UPSTREAM, timeout=15) as resp:
                data = json.load(resp)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as err:
            body = json.dumps({"error": str(err)}).encode()
            self.send_response(502)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return

        versions = data.get("versions") or []
        if ALLOW:
            data["versions"] = [v for v in versions if v.get("name") in ALLOW]
        body = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt: str, *args) -> None:  # noqa: A003
        return


def main() -> None:
    host, _, port_s = LISTEN.partition(":")
    server = ThreadingHTTPServer((host or "0.0.0.0", int(port_s or "9100")), Handler)
    print(
        f"oracle-filter listening on {LISTEN} upstream={UPSTREAM} allow={sorted(ALLOW)}",
        flush=True,
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
