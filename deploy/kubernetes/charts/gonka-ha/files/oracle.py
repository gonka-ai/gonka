"""Read-only, bounded HA protocol projection of DAPI's governance catalog."""

import json
import logging
import os
import re
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

MAX_BYTES = 1024 * 1024
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+~-]{0,63}\Z")


def project(payload, allowed):
    if not isinstance(payload, dict) or not isinstance(payload.get("versions"), list):
        raise ValueError("catalog must contain a versions array")
    seen = set()
    result = []
    for version in payload["versions"]:
        if not isinstance(version, dict):
            raise ValueError("version must be an object")
        name = version.get("name")
        if not isinstance(name, str) or not NAME.fullmatch(name) or name in seen:
            raise ValueError("invalid or duplicate protocol name")
        seen.add(name)
        if name in allowed:
            # Keep governance URLs, digests and other metadata unchanged.
            result.append(version)
    return {**payload, "versions": result}


def handler(upstream, allowed):
    class Handler(BaseHTTPRequestHandler):
        def reply(self, status, body):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/healthz":
                self.reply(200, b'{"status":"live"}')
                return
            if self.path != "/versions":
                self.reply(404, b'{"error":"not found"}')
                return
            try:
                with urllib.request.urlopen(upstream, timeout=5) as response:
                    data = response.read(MAX_BYTES + 1)
                if len(data) > MAX_BYTES:
                    raise ValueError("catalog exceeds size limit")
                body = json.dumps(project(json.loads(data), allowed)).encode()
            except Exception as error:
                # Never turn an upstream failure into an empty desired catalog.
                logging.warning("catalog unavailable: %s", type(error).__name__)
                self.reply(502, b'{"error":"catalog unavailable"}')
                return
            self.reply(200, body)

        def log_message(self, *_args):
            pass

    return Handler


if __name__ == "__main__":
    upstream = os.environ["ORACLE_UPSTREAM"]
    parsed = urlsplit(upstream)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("ORACLE_UPSTREAM must be an HTTP(S) URL")
    allowed = set(os.environ["ORACLE_ALLOW"].split())
    if not allowed or any(not NAME.fullmatch(name) for name in allowed):
        raise ValueError("ORACLE_ALLOW must contain valid HA protocol names")
    if allowed.intersection({"v1", "v2", "v3"}):
        raise ValueError("legacy SQLite protocols cannot enter the HA pool")
    ThreadingHTTPServer(("0.0.0.0", 9100), handler(upstream, allowed)).serve_forever()
