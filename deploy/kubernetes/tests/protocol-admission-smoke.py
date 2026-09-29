#!/usr/bin/env python3
"""Real router images: first publication needs a reserve; accepted routes survive.

Uses isolated Docker resources, rendered Helm admission settings and mock peers.
Run with HELM=/path/to/helm python3 deploy/kubernetes/tests/protocol-admission-smoke.py.
This does not exercise chain, PostgreSQL, inference or Kubernetes scheduling.
"""

import json
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import uuid

from test_protocol_admission import router_settings


ROOT = Path(__file__).resolve().parents[3]
FIXTURE = Path(__file__).with_name("fixtures") / "admission.py"


def command(*args, timeout=120, check=True):
    return subprocess.run(args, text=True, capture_output=True, timeout=timeout, check=check)


def wait_for(label, predicate, timeout=60):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = predicate()
            if last:
                return
        except (subprocess.CalledProcessError, ValueError) as error:
            last = str(error)
        time.sleep(0.25)
    raise AssertionError(f"timed out: {label}; last result: {last}")


def main():
    settings = router_settings()
    task = "gonka-admission-" + uuid.uuid4().hex[:10]
    containers, images, volumes = [], [], []
    network_created = False
    failed = True

    def stop(_signal, _frame):
        raise SystemExit(128 + _signal)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGHUP, stop)
    with tempfile.TemporaryDirectory(prefix="gonka-admission-") as scratch:
        state_file = Path(scratch) / "state.json"

        def state(catalog, first, second, unavailable=False):
            temporary = state_file.with_suffix(".next")
            temporary.write_text(json.dumps({"catalog": catalog,
                                            "ready": {"a": first, "b": second},
                                            "catalog_unavailable": unavailable}))
            temporary.replace(state_file)

        try:
            command("docker", "network", "create", task)
            network_created = True
            state(["v6"], ["v6"], [])
            for peer in ("a", "b"):
                name = f"{task}-peer-{peer}"
                containers.append(name)
                command("docker", "run", "-d", "--name", name, "--network", task,
                        "--network-alias", f"peer-{peer}", "--network-alias", "peer-pool",
                        "--mount", f"type=bind,src={scratch},dst=/state,readonly",
                        "--mount", f"type=bind,src={FIXTURE},dst=/fixture.py,readonly",
                        "python:3.13-alpine", "python3", "/fixture.py", "/state/state.json", peer)

            for component, directory in (("router", "versiond-router"), ("ingress", "proxy-router")):
                state(["v6"], ["v6"], [])
                name = f"{task}-{component}"
                image = name + ":test"
                volume = name + "-catalog"
                containers.append(name)
                images.append(image)
                volumes.append(volume)
                command("docker", "build", "-q", "-t", image, "-f",
                        str(ROOT / directory / "Dockerfile"), str(ROOT), timeout=300)
                command("docker", "volume", "create", volume)
                values = {key: value for key, value in settings[component].items()
                          if isinstance(value, str)}
                # Adapt only infrastructure addresses and polling speed. Protocol,
                # reserve and dynamic-slot settings come from the rendered chart.
                values.update(VERSIOND_ROUTING_CATALOG_URL="http://peer-a:8080/versions",
                              VERSIOND_ROUTING_CATALOG_POLL_SECONDS="1")
                if component == "router":
                    values["VERSIOND_POOL_ENDPOINTS"] = json.dumps([
                        {"id": "a", "host": "peer-a", "port": 8080},
                        {"id": "b", "host": "peer-b", "port": 8080},
                    ])
                    data_port = 8080
                else:
                    values.update(VERSIOND_ROUTER_POOL_HOST="peer-pool",
                                  VERSIOND_ROUTER_FLEET_CAPACITY="2",
                                  PROXY_ROUTER_PUBLIC_BIND_ADDRESS="0.0.0.0",
                                  PROXY_ROUTER_METRICS_BIND_HOST="127.0.0.1")
                    data_port = 18081
                environment = [argument for key, value in values.items()
                               for argument in ("-e", f"{key}={value}")]

                def start():
                    command("docker", "run", "-d", "--name", name, "--network", task,
                            "--user", "99:99", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE",
                            "--security-opt", "no-new-privileges",
                            "--mount", f"type=volume,src={volume},dst=/var/lib/gonka-router",
                            *environment, image)

                def restart():
                    command("docker", "rm", "-f", name)
                    start()

                def http(port, path):
                    return command("docker", "exec", name, "curl", "--silent", "--max-time", "3",
                                   "--output", "/dev/null", "--write-out", "%{http_code}",
                                   f"http://127.0.0.1:{port}{path}").stdout

                def ready(version):
                    return http(8404, "/readyz?version=" + version)

                def catalog_status():
                    return json.loads(command("docker", "exec", name,
                                      "/usr/local/lib/router-runtime/catalog-status", "--state").stdout)

                def pending(version):
                    status = catalog_status()
                    return (status["state"] == "activation-pending" and
                            f"version '{version}' has 1/2 ready backends" in status["detail"])

                def accepted():
                    result = command("docker", "exec", name, "/usr/local/lib/router-runtime/catalog-cache",
                                     "read", "/var/lib/gonka-router/catalog.json", "86400", check=False)
                    if result.returncode not in (0, 2):
                        raise AssertionError("could not read accepted catalog: " + result.stderr)
                    return set(result.stdout.splitlines())

                def assert_unpublished(version):
                    assert ready(version) == "503", f"{component} published readiness for {version}"
                    assert http(data_port, f"/{version}/sessions/admission/chat") == "503", \
                        f"{component} routed unpublished {version}"
                    assert version not in accepted(), f"{component} persisted unready {version}"

                start()
                wait_for(f"{component}: cold-start v6 has one ready peer", lambda: pending("v6"))
                assert_unpublished("v6")
                restart()
                wait_for(f"{component}: restart before admission", lambda: pending("v6"))
                assert_unpublished("v6")
                print(f"PASS {component}: cold start and unaccepted restart reject 1/2 ready peers", flush=True)

                state(["v6"], ["v6"], ["v6"])
                wait_for(f"{component}: admit v6 with two peers", lambda: ready("v6") == "200")
                assert http(data_port, "/v6/sessions/admission/chat") == "200"
                assert accepted() == {"v6"}
                print(f"PASS {component}: two ready peers publish and persist v6", flush=True)

                state(["v6", "v7"], ["v6", "v7"], ["v6"])
                wait_for(f"{component}: pending v7", lambda: pending("v7"))
                assert_unpublished("v7")
                assert ready("v6") == "200"
                assert http(data_port, "/v6/sessions/admission/chat") == "200"
                print(f"PASS {component}: a pending addition does not bypass its reserve or hide v6", flush=True)

                # A stale accepted snapshot remains authoritative for previously
                # admitted routes during a catalog outage, even below the reserve.
                state(["v6", "v7"], ["v6", "v7"], [], unavailable=True)
                command("docker", "exec", name, "sh", "-ec",
                        "jq '.fetched_at_unix = 1' /var/lib/gonka-router/catalog.json > "
                        "/var/lib/gonka-router/stale.json; mv /var/lib/gonka-router/stale.json "
                        "/var/lib/gonka-router/catalog.json")
                restart()
                wait_for(f"{component}: restore accepted v6 below reserve", lambda: ready("v6") == "200")
                assert http(data_port, "/v6/sessions/admission/chat") == "200"
                assert_unpublished("v7")
                assert accepted() == {"v6"}
                print(f"PASS {component}: stale accepted v6 survives restart/outage with one peer; v7 stays blocked", flush=True)

                state(["v6", "v7"], ["v6", "v7"], ["v6", "v7"])
                wait_for(f"{component}: admit v7 after reserve recovers", lambda: ready("v7") == "200")
                assert http(data_port, "/v7/sessions/admission/chat") == "200"
                assert accepted() == {"v6", "v7"}
                print(f"PASS {component}: v7 publishes after two peers become ready", flush=True)
                command("docker", "rm", "-f", name)
            failed = False
        finally:
            if failed:
                for name in containers:
                    result = command("docker", "logs", "--tail", "80", name, check=False)
                    if result.returncode == 0:
                        print(result.stdout + result.stderr, flush=True)
            cleanup_errors = []
            for name in containers:
                result = command("docker", "rm", "-f", name, check=False)
                if result.returncode and "No such container" not in result.stderr:
                    cleanup_errors.append(result.stderr)
            for name in volumes:
                result = command("docker", "volume", "rm", name, check=False)
                if result.returncode:
                    cleanup_errors.append(result.stderr)
            if network_created:
                result = command("docker", "network", "rm", task, check=False)
                if result.returncode:
                    cleanup_errors.append(result.stderr)
            for image in images:
                result = command("docker", "image", "rm", image, check=False)
                if result.returncode and "No such image" not in result.stderr:
                    cleanup_errors.append(result.stderr)
            if cleanup_errors:
                raise RuntimeError("could not clean owned admission resources: " + "\n".join(cleanup_errors))
    print("PASS chart protocol admission smoke; isolated Docker resources removed", flush=True)


if __name__ == "__main__":
    main()
