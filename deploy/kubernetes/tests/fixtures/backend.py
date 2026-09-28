"""Mock application boundaries for the real Kubernetes routing smoke test.

This deliberately does not run devshard, PostgreSQL, chain consensus, or ML.
The external mode also forwards test traffic through cluster networking so
kubectl port-forward never bypasses the public router's PodIP-only listener.
"""

import json
import os
import signal
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


MODE = (
    "versiond" if os.environ.get("VERSIOND_BINARY_NAME") else
    "edge-api" if os.environ.get("EDGE_API_PORT") else "external"
)
PORT = {"versiond": 8080, "edge-api": 18080, "external": 9000}[MODE]
OWNER = os.environ.get("HOSTNAME", "mock")
READY = threading.Event()
STOPPING = threading.Event()
if MODE != "versiond":
    READY.set()


class Server(ThreadingHTTPServer):
    daemon_threads = False


class Handler(BaseHTTPRequestHandler):
    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        if length > 1024 * 1024:
            self.reply(413, {"error": "fixture body too large"})
            return
        self.request_body = self.rfile.read(length)
        self.do_GET()

    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        if MODE == "external" and parsed.path.startswith("/proxy/"):
            self.proxy(parsed)
            return
        if parsed.path == "/versions":
            self.reply(200, {"versions": [
                {"name": name, "binary": "http://fixture:9000/mock.zip", "sha256": "0" * 64}
                for name in ("v1", "v6")
            ]})
            return
        if parsed.path == "/healthz":
            self.reply(200, {"component": MODE, "owner": OWNER})
            return
        if parsed.path == "/readyz" or parsed.path == "/v6/healthz":
            version = urllib.parse.parse_qs(parsed.query).get("version", ["v6"])[0]
            self.reply(200 if READY.is_set() and version == "v6" else 503, {"ready": READY.is_set()})
            return
        if MODE == "versiond" and not READY.is_set() and not STOPPING.is_set():
            self.reply(503, {"error": "mock oracle not ready"})
            return
        if parsed.path.endswith("/stream"):
            self.stream(parsed)
            return
        self.reply(200, {
            "component": MODE, "owner": OWNER, "path": parsed.path,
            "ha": self.headers.get("Devshard-Ha"),
        })

    def stream(self, parsed):
        seconds = min(30, max(1, int(urllib.parse.parse_qs(parsed.query).get("seconds", ["12"])[0])))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            for tick in range(seconds + 1):
                event = "complete" if tick == seconds else "tick"
                self.wfile.write(("data: " + json.dumps({"event": event, "tick": tick, "owner": OWNER}) + "\n\n").encode())
                self.wfile.flush()
                if tick != seconds:
                    time.sleep(1)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def proxy(self, parsed):
        _, _, target, *parts = parsed.path.split("/")
        prefix = os.environ["TEST_RELEASE_PREFIX"]
        namespace = os.environ["TEST_NAMESPACE"]
        suffix = f"{namespace}.svc.cluster.local"
        if target == "ingress":
            host = f"{prefix}-ingress.{suffix}"
        elif target in {f"ingress-{i}" for i in range(3)}:
            host = f"{prefix}-{target}.{prefix}-ingress-headless.{suffix}"
        elif target in {f"router-{i}" for i in range(3)}:
            host = f"{prefix}-{target}.{prefix}-router.{suffix}:8080"
        else:
            self.reply(400, {"error": "unknown fixture proxy target"})
            return
        url = "http://" + host + "/" + "/".join(parts)
        if parsed.query:
            url += "?" + parsed.query
        try:
            request = urllib.request.Request(
                url, data=getattr(self, "request_body", None), method=self.command,
                headers={"Devshard-Ha": "false", "Content-Type": "application/json"},
            )
            response = urllib.request.urlopen(request, timeout=40)
        except urllib.error.HTTPError as error:
            response = error
        except (OSError, urllib.error.URLError) as error:
            self.reply(502, {"error": type(error).__name__})
            return
        with response:
            self.send_response(response.status)
            self.send_header("Content-Type", response.headers.get("Content-Type", "application/octet-stream"))
            self.send_header("Connection", "close")
            self.end_headers()
            try:
                while chunk := response.read1(8192):
                    self.wfile.write(chunk)
                    self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass

    def log_message(self, *_args):
        pass


def await_oracle():
    while not STOPPING.is_set():
        try:
            with urllib.request.urlopen(os.environ["VERSIOND_ORACLE_URL"], timeout=3) as response:
                names = {entry["name"] for entry in json.load(response)["versions"]}
            if names == {"v6"}:
                READY.set()
                return
        except (OSError, KeyError, ValueError):
            pass
        STOPPING.wait(1)


def main():
    server = Server(("0.0.0.0", PORT), Handler)

    def stop(_signal, _frame):
        if STOPPING.is_set():
            return
        STOPPING.set()
        READY.clear()

        def drain():
            if MODE == "versiond":
                time.sleep(float(os.environ.get("VERSIOND_DRAIN_ANNOUNCE", "5s").removesuffix("s")))
            server.shutdown()

        threading.Thread(target=drain).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    if MODE == "versiond":
        threading.Thread(target=await_oracle, daemon=True).start()
    print(f"fixture {MODE} {OWNER} listening on {PORT}", flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
