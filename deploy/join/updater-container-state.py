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


if __name__ == "__main__":
    try:
        if sys.argv[1] == "capture":
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
