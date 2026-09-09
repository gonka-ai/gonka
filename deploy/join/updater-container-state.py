#!/usr/bin/env python3
"""Capture/restore a join service's actual Docker specification.

The Docker CLI's dial-stdio transport follows its current daemon/context. The
create request reuses Docker's own Config/HostConfig instead of translating a
subset back into Compose options. Mounted application/database data is never
copied or rolled back here.
"""

import copy
import http.client
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.parse
import shutil
import uuid
import posixpath
import tempfile

DOCKER = os.environ.get("DOCKER_BIN", "docker")
WAIT = int(os.environ.get("UPDATE_WAIT_TIMEOUT_SECONDS", "2100"))


def docker(*args, timeout=60):
    return subprocess.run([DOCKER, *args], check=True, text=True,
                          capture_output=True, timeout=timeout).stdout


def inspect(name):
    try:
        return json.loads(docker("inspect", "--type", "container", name))[0]
    except subprocess.CalledProcessError as error:
        if "no such object" in error.stderr.lower() or "no such container" in error.stderr.lower():
            return None
        raise RuntimeError(f"cannot inspect {name}: {error.stderr.strip()}") from error


def create(config, name):
    body = json.dumps(config).encode()
    request = (f"POST /containers/create?name={urllib.parse.quote(name, safe='')} HTTP/1.1\r\n"
               f"Host: docker\r\nContent-Type: application/json\r\nContent-Length: {len(body)}\r\n"
               "Connection: close\r\n\r\n").encode() + body
    reply = subprocess.run([DOCKER, "system", "dial-stdio"], input=request,
                           capture_output=True, check=True, timeout=60).stdout

    class Socket:
        def makefile(self, *_args):
            return io.BytesIO(reply)

    response = http.client.HTTPResponse(Socket())
    response.begin()
    result = json.loads(response.read())
    if response.status != 201:
        raise RuntimeError(f"Docker could not restore {name}: {result.get('message', response.status)}")
    return result["Id"]


def save(path, project, name, container_id):
    record = inspect(container_id) if container_id else None
    if container_id and not record:
        raise RuntimeError(f"container {container_id} disappeared while saving its rollback record")
    if record:
        if record["Config"]["Labels"].get("com.docker.compose.project") != project:
            raise RuntimeError(f"{name} belongs to another Compose project")
        record["GonkaProject"] = project
    else:
        record = {"Name": name, "GonkaProject": project, "Absent": True}
    with open(path, "x", encoding="utf-8") as output:
        json.dump(record, output)
        output.flush()
        os.fsync(output.fileno())


def restored_config(record):
    config = copy.deepcopy(record["Config"])
    config["Image"] = record["Image"]  # immutable, pinned by the rollback tag
    host = copy.deepcopy(record["HostConfig"])
    # Preserve allocated host ports, including originally ephemeral bindings.
    ports = record["NetworkSettings"].get("Ports")
    if ports:
        host["PortBindings"] = {key: value for key, value in ports.items() if value}
    # Docker image VOLUME declarations otherwise allocate new anonymous data.
    host["Mounts"] = host.get("Mounts") or []
    volumes = {mount["Destination"]: mount["Name"] for mount in record.get("Mounts", [])
               if mount["Type"] == "volume"}
    for mount in host["Mounts"]:
        if mount["Type"] == "volume" and not mount.get("Source"):
            mount["Source"] = volumes[mount["Target"]]
    mounted = {mount["Target"] for mount in host["Mounts"]}
    mounted.update(binding.split(":")[1] for binding in host.get("Binds") or [])
    for mount in record.get("Mounts", []):
        if mount["Type"] == "volume" and mount["Destination"] not in mounted:
            host.setdefault("Mounts", []).append({
                "Type": "volume", "Source": mount["Name"],
                "Target": mount["Destination"], "ReadOnly": not mount["RW"],
            })
    config["HostConfig"] = host
    endpoints = {}
    for name, endpoint in record["NetworkSettings"].get("Networks", {}).items():
        endpoints[name] = {key: value for key, value in endpoint.items()
                           if key in ("IPAMConfig", "Links", "Aliases", "DriverOpts", "GwPriority")
                           and value is not None}
    config["NetworkingConfig"] = {"EndpointsConfig": endpoints}
    return config


def wait_healthy(container_id):
    deadline = time.monotonic() + WAIT
    while time.monotonic() < deadline:
        current = inspect(container_id)
        if not current or not current["State"]["Running"]:
            raise RuntimeError("restored container is not running")
        health = current["State"].get("Health", {}).get("Status", "healthy")
        if health == "healthy":
            return
        if health == "unhealthy":
            raise RuntimeError("restored container is unhealthy")
        time.sleep(0.5)
    raise RuntimeError("restored container did not become healthy before the deadline")


def restore(path, wait=True):
    record = json.loads(Path(path).read_text())
    name = record["Name"].lstrip("/")
    current = inspect(name)
    if current and current["Config"]["Labels"].get("com.docker.compose.project") != record["GonkaProject"]:
        raise RuntimeError(f"refusing to replace {name}: it belongs to another deployment")
    if current and current["Id"] != record.get("Id"):
        docker("stop", current["Id"], timeout=WAIT + 60)
        docker("rm", current["Id"])
        current = None
    if record.get("Absent"):
        return
    container_id = current["Id"] if current else create(restored_config(record), name)
    if record["State"]["Running"]:
        docker("start", container_id)
        if wait:
            wait_healthy(container_id)
    elif current and current["State"]["Running"]:
        docker("stop", container_id, timeout=WAIT + 60)


def sync_directory(path):
    descriptor = os.open(path, os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish(source, target):
    for path in Path(source).iterdir():
        with path.open("rb") as record:
            os.fsync(record.fileno())
    sync_directory(source)
    os.rename(source, target)
    sync_directory(Path(target).parent)


def commit(path):
    pending = Path(path)
    if pending.exists():
        garbage = pending.with_name("committed-" + uuid.uuid4().hex)
        os.rename(pending, garbage)
        sync_directory(pending.parent)
        shutil.rmtree(garbage)



def pg_storage(model, container_id):
    candidate = model["services"]["devshard-postgres"]
    record = inspect(container_id) if container_id else None
    if not record:
        return "fresh", None
    if os.environ.get("UPDATE_ACCEPT_DATABASE_CHANGE") == "true":
        return "offline", record
    environment = dict(item.split("=", 1) for item in record["Config"].get("Env", []) if "=" in item)
    old_data = posixpath.normpath(environment.get("PGDATA", "/var/lib/postgresql/data"))
    new_data = posixpath.normpath(candidate.get("environment", {}).get("PGDATA", "/var/lib/postgresql/gonka/data"))

    def location(data, mounts):
        matches = [m for m in mounts if data == m["target"] or data.startswith(m["target"].rstrip("/") + "/")]
        if not matches:
            raise RuntimeError("cannot identify the PostgreSQL data mount; refusing to replace it")
        mount = max(matches, key=lambda m: len(m["target"]))
        return mount["type"], posixpath.normpath(mount["source"]), posixpath.relpath(data, mount["target"])

    old_mounts = [{"type": m["Type"], "source": m.get("Name") or m["Source"], "target": m["Destination"]}
                  for m in record.get("Mounts", [])]
    new_mounts = []
    for mount in candidate.get("volumes", []):
        item = dict(mount)
        if item["type"] == "volume":
            item["source"] = model.get("volumes", {}).get(item.get("source"), {}).get("name", item.get("source", ""))
        if item.get("source"):
            new_mounts.append(item)
    old_location, new_location = location(old_data, old_mounts), location(new_data, new_mounts)
    if old_location == new_location:
        return "same", record
    if (old_data == "/var/lib/postgresql/data" and old_location[0] == "volume" and
            new_data == "/var/lib/postgresql/gonka/data" and new_location[0] == "bind" and
            not any(m["target"] == "/var/lib/postgresql/gonka" for m in old_mounts)):
        return "migrate", record
    raise RuntimeError("PostgreSQL PGDATA or its mount changed; keep the running data directory for an ordinary update, or use the verified offline database-change procedure")


def postgres_connection(model):
    first = next(v for k, v in model["services"].items() if k == "versiond" or (k.startswith("versiond") and k[8:].isdigit()))
    return {k: v.replace("$$", "$") for k, v in first.get("environment", {}).items()
            if k in ("PGUSER", "PGDATABASE", "PGPASSWORD", "PGPORT") and v}


def seal_postgres(container_id):
    arguments = []
    for key, value in postgres_connection(json.load(sys.stdin)).items():
        arguments += ["-e", key + "=" + value]
    nonce = str(uuid.uuid4())
    query = ("BEGIN; CREATE TABLE IF NOT EXISTS public.gonka_updater_continuity "
             "(singleton boolean PRIMARY KEY CHECK (singleton), nonce uuid NOT NULL); "
             f"INSERT INTO public.gonka_updater_continuity VALUES (true, '{nonce}') "
             "ON CONFLICT (singleton) DO UPDATE SET nonce=EXCLUDED.nonce; COMMIT; "
             "SELECT nonce FROM public.gonka_updater_continuity WHERE singleton")
    observed = docker("exec", *arguments, container_id, "psql", "-XwqAt", "-h", "127.0.0.1",
                      "-v", "ON_ERROR_STOP=1", "-c", query).strip()
    if observed != nonce:
        raise RuntimeError("cannot seal the running PostgreSQL history before replacement")
    print(nonce)


def prepare_postgres(path, project, container_id, nonce, working_dir=None, config_files=None):
    model = json.load(sys.stdin)
    mode, previous = pg_storage(model, container_id)
    service = copy.deepcopy(model["services"]["devshard-postgres"])
    # Compose owns its built-in provenance labels and overwrites them with
    # the temporary recovery file. Keep canonical discovery paths separately,
    # on the container from creation onward (including interrupted recovery).
    previous_labels = (previous or {}).get("Config", {}).get("Labels", {})
    discovery = {"working_dir": working_dir, "config_files": config_files}
    for key, value in discovery.items():
        label = "ai.gonka.updater.compose." + key
        value = value or previous_labels.get(label) or previous_labels.get("com.docker.compose.project." + key)
        if not value:
            raise RuntimeError("cannot save canonical Compose discovery paths for PostgreSQL recovery")
        service.setdefault("labels", {})[label] = value
    # Freeze both the candidate image and the retained v4 volume. Compose
    # cannot inherit an anonymous volume after its old container disappears.
    service["image"] = docker("image", "inspect", "--format", "{{.Id}}", service["image"]).strip()
    service.pop("depends_on", None)
    volumes = copy.deepcopy(model.get("volumes", {}))
    mounted = {m["target"] for m in service.get("volumes", [])}
    for mount in (previous or {}).get("Mounts", []):
        if mount["Type"] == "volume" and mount["Destination"] not in mounted:
            key = "gonka-update-volume-" + str(len(volumes))
            volumes[key] = {"external": True, "name": mount["Name"]}
            service.setdefault("volumes", []).append({"type": "volume", "source": key, "target": mount["Destination"]})
    frozen = {k: copy.deepcopy(model[k]) for k in ("networks", "configs", "secrets") if k in model}
    frozen.update(name=project, services={"devshard-postgres": service}, volumes=volumes)
    connection = postgres_connection(model)
    record = {"Model": frozen, "Project": project, "Nonce": nonce,
              "Connection": connection, "PreviousImage": (previous or {}).get("Image"), "Mode": mode}
    with open(path, "x") as output:
        json.dump(record, output)
        output.flush()
        os.fsync(output.fileno())


def run_postgres(path, recover=False, verify_only=False):
    record = json.loads(Path(path).read_text())
    model = record["Model"]
    service = model["services"]["devshard-postgres"]
    # Do not load today's config.env on recovery: it may name another PGDATA.
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json", dir=Path(path).parent) as frozen:
        json.dump(model, frozen)
        frozen.flush()

        def up(image):
            service["image"] = image
            frozen.seek(0)
            json.dump(model, frozen)
            frozen.truncate()
            frozen.flush()
            docker("compose", "--project-name", record["Project"], "-f", frozen.name,
                   "up", "-d", "--no-deps", "--wait", "--wait-timeout", str(WAIT), "devshard-postgres", timeout=WAIT + 60)
        try:
            if not verify_only:
                up(service["image"])
        except subprocess.SubprocessError:
            if not recover or not record.get("PreviousImage"):
                raise RuntimeError("PostgreSQL replacement failed; its durable recovery record is retained") from None
            up(record["PreviousImage"])
        container_id = docker("compose", "--project-name", record["Project"], "-f", frozen.name,
                              "ps", "--all", "--quiet", "devshard-postgres").strip()
        arguments = []
        for key, value in record["Connection"].items():
            arguments += ["-e", key + "=" + value]
        nonce = record["Nonce"]
        query = "SELECT NOT pg_is_in_recovery()"
        if nonce:
            query = "SELECT nonce FROM public.gonka_updater_continuity WHERE singleton AND NOT pg_is_in_recovery()"
        try:
            observed = docker("exec", *arguments, container_id, "psql", "-XwqAt", "-h", "127.0.0.1",
                              "-v", "ON_ERROR_STOP=1", "-c", query).strip()
            if observed != (nonce or "t"):
                raise RuntimeError("PostgreSQL history check failed")
        except (subprocess.SubprocessError, RuntimeError):
            docker("stop", container_id, timeout=WAIT + 60)
            raise RuntimeError("the started PostgreSQL did not preserve the saved history challenge; stopped it and retained pending recovery") from None


if __name__ == "__main__":
    try:
        if sys.argv[1] == "postgres-storage":
            print(pg_storage(json.load(sys.stdin), sys.argv[2])[0])
        elif sys.argv[1] == "postgres-seal":
            seal_postgres(sys.argv[2])
        elif sys.argv[1] == "postgres-verify":
            run_postgres(sys.argv[2], verify_only=True)
        elif sys.argv[1] == "postgres-prepare":
            prepare_postgres(*sys.argv[2:])
        elif sys.argv[1] in ("postgres-start", "postgres-recover"):
            run_postgres(sys.argv[2], recover=sys.argv[1] == "postgres-recover")
        elif sys.argv[1] == "capture":
            save(*sys.argv[2:])
        elif sys.argv[1] == "restore":
            restore(sys.argv[2])
        elif sys.argv[1] == "restore-start":
            restore(sys.argv[2], wait=False)
        elif sys.argv[1] == "restore-wait":
            record = json.loads(Path(sys.argv[2]).read_text())
            if not record.get("Absent") and record["State"]["Running"]:
                wait_healthy(record["Name"].lstrip("/"))
        elif sys.argv[1] == "publish":
            publish(*sys.argv[2:])
        elif sys.argv[1] == "commit":
            commit(sys.argv[2])
        else:
            raise RuntimeError("expected capture or restore")
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        # Subprocess arguments can contain a complete container specification.
        # Never print them or request bodies (they can contain credentials).
        message = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        print(f"updater rollback: {message}", file=sys.stderr)
        sys.exit(1)
