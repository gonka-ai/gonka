#!/usr/bin/env python3
"""Real PostgreSQL checks for the updater's durable history/recovery boundary."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent
HELPER = ROOT / "updater-container-state.py"


def run(*args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, **kwargs)


def checked(*args, **kwargs):
    result = run(*args, **kwargs)
    if result.returncode:
        raise AssertionError(f"{args[0]} failed: {result.stderr}")
    return result.stdout.strip()


with tempfile.TemporaryDirectory(prefix="gonka-updater-pg-") as temp:
    directory = Path(temp)
    project = "gonka-updater-pg-" + uuid.uuid4().hex[:10]
    data = directory / "data"
    data.mkdir()
    model = {
        "name": project,
        "services": {
            "devshard-postgres": {
                "image": "postgres:16-alpine",
                "environment": {"PGDATA": "/var/lib/postgresql/gonka/data",
                                "POSTGRES_USER": "devshardd", "POSTGRES_DB": "devshardd",
                                "POSTGRES_PASSWORD": "test-only"},
                "volumes": [{"type": "bind", "source": str(data), "target": "/var/lib/postgresql/gonka"}],
                "healthcheck": {"test": ["CMD", "pg_isready", "-U", "devshardd"],
                                "interval": "1s", "timeout": "3s", "retries": 60},
            },
            "versiond": {"image": "busybox:1.37", "environment": {
                "PGUSER": "devshardd", "PGDATABASE": "devshardd", "PGPASSWORD": "test-only"}},
        },
    }
    compose_file = directory / "compose.json"
    compose_file.write_text(json.dumps(model))
    compose = ("docker", "compose", "-p", project, "-f", str(compose_file))

    def helper(command, *args, model_input=model):
        return run("python3", str(HELPER), command, *args, input=json.dumps(model_input),
                   env=dict(os.environ, UPDATE_WAIT_TIMEOUT_SECONDS="90"))

    def sql(container, query):
        return checked("docker", "exec", container, "psql", "-U", "devshardd", "-d", "devshardd",
                       "-XwqAt", "-v", "ON_ERROR_STOP=1", "-c", query)

    try:
        checked(*compose, "up", "-d", "--wait", "devshard-postgres")
        container = checked(*compose, "ps", "-q", "devshard-postgres")
        for _ in range(60):
            attempt = helper("postgres-seal", container)
            if attempt.returncode == 0:
                break
            time.sleep(1)
        assert attempt.returncode == 0, attempt.stderr
        nonce = attempt.stdout.strip()
        sql(container, "CREATE TABLE retained (value int); INSERT INTO retained VALUES (42)")

        for kind in ("mount", "pgdata"):
            changed = copy.deepcopy(model)
            service = changed["services"]["devshard-postgres"]
            if kind == "mount":
                service["volumes"][0]["source"] = str(directory / "old-backup")
            else:
                service["environment"]["PGDATA"] += "-backup"
            result = helper("postgres-storage", container, model_input=changed)
            assert result.returncode and "PGDATA or its mount changed" in result.stderr, result.stderr
            assert checked(*compose, "ps", "-q", "devshard-postgres") == container
        print("updater-postgres_test: mount and PGDATA changes refused before replacement", flush=True)

        journal = directory / "postgres.json"
        result = helper("postgres-prepare", str(journal), project, container, nonce)
        assert result.returncode == 0, result.stderr
        assert helper("postgres-verify", str(journal)).returncode == 0
        checked("docker", "stop", container)
        checked("docker", "rm", container)
        # Today's Compose file is deliberately wrong. Recovery has to use its
        # saved target and immutable image, not these new configuration values.
        compose_file.write_text(json.dumps(changed))
        result = helper("postgres-recover", str(journal))
        assert result.returncode == 0, result.stderr
        container = checked(*compose, "ps", "-q", "devshard-postgres")
        assert sql(container, "SELECT value FROM retained") == "42"
        print("updater-postgres_test: removed container recovered on the saved target", flush=True)

        # A broken candidate image may fall back to the previous executable,
        # while keeping the saved target and all writes already committed there.
        if run("docker", "image", "inspect", "busybox:1.36").returncode:
            checked("docker", "pull", "busybox:1.36")
        fallback_model = copy.deepcopy(model)
        fallback_model["services"]["devshard-postgres"]["image"] = "busybox:1.36"
        fallback_journal = directory / "fallback.json"
        result = helper("postgres-prepare", str(fallback_journal), project, container, nonce,
                        model_input=fallback_model)
        assert result.returncode == 0, result.stderr
        result = helper("postgres-recover", str(fallback_journal))
        assert result.returncode == 0, result.stderr
        container = checked(*compose, "ps", "-q", "devshard-postgres")
        assert sql(container, "SELECT value FROM retained") == "42"
        print("updater-postgres_test: failed candidate recovered with previous image on same data", flush=True)

        # A valid writable database with a different history must fail after
        # start; a compose healthcheck alone cannot close the pending step.
        sql(container, "UPDATE public.gonka_updater_continuity SET nonce = '00000000-0000-0000-0000-000000000001'")
        result = helper("postgres-verify", str(journal))
        assert result.returncode and "history challenge" in result.stderr, result.stderr
        assert checked("docker", "inspect", "--format", "{{.State.Running}}", container) == "false"
        assert journal.exists()
        print("updater-postgres_test: wrong history stopped; recovery record retained", flush=True)
    finally:
        ids = checked("docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={project}").split()
        if ids:
            checked("docker", "rm", "-fv", *ids)
        checked(*compose, "down", "--volumes", "--remove-orphans")
        checked("docker", "run", "--rm", "-v", f"{data}:/cleanup", "--entrypoint", "sh",
                "postgres:16-alpine", "-c", "rm -rf /cleanup/data")
print("updater-postgres_test: ok")
