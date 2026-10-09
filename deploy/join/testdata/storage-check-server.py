"""Versiond proof API stand-in backed by real PostgreSQL for the updater test."""

import http.server
import json
import os
from pathlib import Path
import subprocess
import uuid


def sql(database, statement, readonly=False):
    env = dict(os.environ, PGDATABASE=database)
    if readonly:
        env["PGOPTIONS"] = "-c default_transaction_read_only=on"
    return subprocess.check_output(
        ["psql", "-XwqAt", "-v", "ON_ERROR_STOP=1", "-c", statement],
        env=env, text=True, timeout=10,
    ).strip()


class Handler(http.server.BaseHTTPRequestHandler):
    written = set()

    def log_message(self, *args):
        pass

    def reply(self, status, payload):
        self.send_response(status)
        self.end_headers()
        self.wfile.write(json.dumps(payload).encode())

    def settings(self):
        return json.loads(Path("/fixture/settings.json").read_text())

    def proof(self, settings):
        token = settings["token"]
        if settings.get("mode") == "stale" and token in self.written:
            token += "-changed"
        return {
            "identity": sql(settings["databases"][0],
                            "SELECT identity FROM devshard_storage_identity WHERE singleton"),
            "snapshot": token,
            "children": len(settings["databases"]),
            "targets": [{"generation": str(i), "version": f"v{i + 5}"}
                        for i in range(len(settings["databases"]))],
        }

    def do_GET(self):
        settings = self.settings()
        mode = settings.get("mode")
        if self.path == "/readyz":
            return self.reply(503 if mode == "unready" else 200, "ready")
        if self.path != "/internal/storage-identity" or mode == "legacy":
            return self.reply(404, "not found")
        if mode == "unavailable":
            return self.reply(503, "proof unavailable")
        proof = self.proof(settings)
        if mode == "identity-only":
            proof = {"identity": proof["identity"]}
        elif mode == "empty":
            proof.update(children=0, targets=[])
        elif mode == "duplicate":
            proof["targets"][1] = proof["targets"][0]
        elif mode == "incomplete":
            proof["children"] += 1
        self.reply(200, proof)

    def do_POST(self):
        settings = self.settings()
        if self.path != "/internal/storage-challenge":
            return self.reply(404, "not found")
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        generation = request["generation"]
        if request["snapshot"] != settings["token"] or request["operation"] != "write":
            return self.reply(503, "stale snapshot")
        nonce = str(uuid.UUID(request["nonce"]))
        try:
            identity = sql(settings["databases"][int(generation)],
                           "UPDATE devshard_storage_identity SET challenge = '" + nonce +
                           "'::uuid WHERE singleton RETURNING identity::text",
                           readonly=settings.get("mode") == "readonly")
        except subprocess.CalledProcessError:
            return self.reply(503, "write failed")
        self.written.add(settings["token"])
        response = dict(identity=identity, found=True, snapshot=settings["token"],
                        generation=generation)
        if settings.get("mode") == "bad-response":
            response.pop("generation")
        self.reply(200, response)


http.server.HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
