"""Mock application boundaries for the real Kubernetes routing smoke test.

This deliberately does not run devshard, PostgreSQL, chain consensus, or ML.
The external mode also forwards test traffic through cluster networking so
kubectl port-forward never bypasses the public router's PodIP-only listener.
"""

import json
import os
from pathlib import Path
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


def served_protocols():
    # The rollout regression changes only fixture-owned state via kubectl exec.
    # Keep it on the replica PVC so replacement cannot accidentally heal a
    # missing protocol and hide an unsafe attempt to stop its last other owner.
    path = Path("/opt/versiond/gonka-fixture-protocols.json")
    versions = set(json.loads(path.read_text())) if path.exists() else {"v6", "v7"}
    # The chart intentionally accepts only observability extra env variables.
    # Interpret one fixture-only attribute to make a replacement coarse-ready
    # while a formerly available protocol is broken, without changing images.
    if "gonka.fixture.drop_protocol=v7" in os.environ.get("OTEL_RESOURCE_ATTRIBUTES", "").split(","):
        versions.discard("v7")
    return versions


class Server(ThreadingHTTPServer):
    daemon_threads = False
    # Three routers perform simultaneous two-connection health checks while the
    # placement regression sends application requests. Python's default queue
    # of five can itself create transient failed checks and false ring changes.
    request_queue_size = 128


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
                for name in ("v1", "v6", "v7")
            ]})
            return
        if parsed.path == "/healthz":
            if MODE == "versiond":
                self.reply(200, [{"name": version, "port": 9200 + ordinal, "status": "running"}
                                 for ordinal, version in enumerate(sorted(served_protocols()))])
            else:
                self.reply(200, {"component": MODE, "owner": OWNER})
            return
        if parsed.path == "/readyz" or parsed.path in ("/v6/healthz", "/v7/healthz"):
            version = urllib.parse.parse_qs(parsed.query).get("version", [""])[0]
            if parsed.path != "/readyz":
                version = parsed.path.split("/")[1]
            ready = READY.is_set() and (not version or version in served_protocols())
            self.reply(200 if ready else 503, {"ready": ready})
            return
        if MODE == "versiond" and not READY.is_set() and not STOPPING.is_set():
            self.reply(503, {"error": "mock oracle not ready"})
            return
        if MODE == "versiond" and parsed.path.split("/")[1] in {"v6", "v7"}:
            if parsed.path.split("/")[1] not in served_protocols():
                self.reply(503, {"error": "fixture protocol unavailable"})
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
            if "v6" in names and names <= {"v6", "v7"}:
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
