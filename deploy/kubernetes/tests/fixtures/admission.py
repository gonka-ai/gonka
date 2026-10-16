"""Controlled mock versiond/router peers for real-image admission checks."""

import json
from pathlib import Path
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit


state_file = Path(sys.argv[1])
peer = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        state = json.loads(state_file.read_text())
        parsed = urlsplit(self.path)
        ready = state["ready"][peer]
        status = 200
        body = {"peer": peer}
        if parsed.path == "/versions":
            status = 503 if state.get("catalog_unavailable") else 200
            body = {"versions": [{"name": name} for name in state["catalog"]]}
        elif parsed.path == "/readyz":
            version = parse_qs(parsed.query).get("version", [None])[0]
            status = 200 if (version in ready if version else bool(ready)) else 503
        elif parsed.path != "/healthz":
            version = parsed.path.split("/")[1]
            status = 200 if version in ready else 503
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_):
        pass


for port in (8080, 8404):
    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
threading.Event().wait()
