"""Exercise guarded upgrades against an isolated live API and real HAProxy.

Application responses remain mock data. This proves membership coordination,
per-protocol admission and restoration, not devshard or database correctness.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import copy
import csv
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    for option in ("kubeconfig", "context", "namespace", "base-url", "prefix", "values", "release", "helm"):
        parser.add_argument("--" + option, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    kubectl = ["kubectl", "--kubeconfig", args.kubeconfig, "--context", args.context,
               "--request-timeout=15s", "-n", args.namespace]
    selector = f"app.kubernetes.io/name=gonka-ha,app.kubernetes.io/instance={args.release}"
    helm = [args.helm, "--kubeconfig", args.kubeconfig, "--kube-context", args.context,
            "--namespace", args.namespace]
    baseline = json.loads(Path(args.values).read_text())

    def run(command, *, check=True, timeout=180, env=None):
        result = subprocess.run(command, text=True, capture_output=True, timeout=timeout, env=env)
        if check and result.returncode:
            raise AssertionError(f"command failed ({result.returncode}): {' '.join(command)}\n"
                                 f"{result.stdout}\n{result.stderr}")
        return result

    def kube(*command, **kwargs):
        return run(kubectl + list(command), **kwargs)

    def obj(kind, name):
        return json.loads(kube("get", kind, name, "-o", "json").stdout)

    def wait(description, predicate, timeout=120):
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            try:
                last = predicate()
                if last:
                    return last
            except (AssertionError, OSError, ValueError) as error:
                last = str(error)
            time.sleep(1)
        raise AssertionError(f"timed out waiting for {description}: {last}")

    def snapshot():
        pods = json.loads(kube("get", "pods", "-l", selector, "-o", "json").stdout)["items"]
        services = json.loads(kube("get", "services", "-l", selector, "-o", "json").stdout)["items"]
        assert all(not p["metadata"].get("deletionTimestamp") for p in pods), "unexpected terminating pod"
        return {
            "pods": {p["metadata"]["name"]: p["metadata"]["uid"] for p in pods},
            "services": {s["metadata"]["name"]: (s["metadata"]["uid"], s["spec"].get("clusterIP"))
                         for s in services},
        }

    def unchanged(before):
        # Give the StatefulSet controller several opportunities to act. Checking
        # Helm's exit alone would miss a scale-down already handed to it.
        for _ in range(5):
            after = snapshot()
            assert after == before, f"rejected/OnDelete upgrade mutated live pods or Services: {before} -> {after}"
            time.sleep(1)

    def values_file(label, values):
        path = Path(args.values).with_name(f"rollout-{label}.json")
        path.write_text(json.dumps(values))
        return str(path)

    def raw_upgrade(path, *, accepted=False):
        result = run(helm + ["upgrade", args.release, str(root / "charts/gonka-ha"),
                             "--values", path, "--timeout", "120s"], check=False)
        if accepted:
            assert result.returncode == 0, result.stdout + result.stderr
        else:
            assert result.returncode != 0, "uncoordinated membership change unexpectedly succeeded"
            assert "maintenance" in result.stderr.lower(), result.stdout + result.stderr
        return result

    def rollout(mode, path, *, success=True, resume=False, timeout=150):
        command = [sys.executable, str(root / "rollout.py"), mode,
                   "--namespace", args.namespace, "--release", args.release,
                   "--kubeconfig", args.kubeconfig, "--context", args.context,
                   "--values", path, "--timeout", str(timeout)]
        if resume:
            command.append("--resume")
        environment = dict(os.environ, HELM=args.helm)
        stop_monitor = threading.Event()
        observations = []
        monitor_errors = []

        def monitor_membership():
            while not stop_monitor.is_set():
                try:
                    pods = json.loads(kube("get", "pods", "-l", selector +
                                           ",app.kubernetes.io/component=router", "-o", "json").stdout)["items"]
                    contracts = set()
                    for pod in pods:
                        # Terminating Pods remain participants until gone: their
                        # accepted streams may still use the previous placement.
                        router = next(c for c in pod["spec"]["containers"] if c["name"] == "router")
                        endpoints = next(e["value"] for e in router["env"]
                                         if e["name"] == "VERSIOND_POOL_ENDPOINTS")
                        contracts.add(json.dumps(json.loads(endpoints), sort_keys=True))
                    observations.append(contracts)
                    if len(contracts) > 1:
                        monitor_errors.append(f"mixed placement contracts among live/terminating router Pods: {contracts}")
                except Exception as error:
                    monitor_errors.append(str(error))
                stop_monitor.wait(0.5)

        monitor = None
        if mode == "maintenance-upgrade":
            monitor = threading.Thread(target=monitor_membership)
            monitor.start()
        try:
            result = run(command, check=False, timeout=1200, env=environment)
        finally:
            stop_monitor.set()
            if monitor:
                monitor.join(timeout=25)
                assert not monitor.is_alive(), "membership observer did not stop"
        print(result.stdout, end="", flush=True)
        if monitor:
            assert not monitor_errors, monitor_errors
            assert any(not contracts for contracts in observations), (
                "maintenance never removed all old router Pods\n" + result.stderr)
        if success:
            assert result.returncode == 0, result.stdout + result.stderr
        else:
            assert result.returncode != 0, "unsafe candidate unexpectedly completed"
            print(result.stderr, end="", flush=True)
        return result

    def get(path):
        with urllib.request.urlopen(args.base_url + path, timeout=15) as response:
            return json.load(response)

    def healthy_routes():
        for version in ("v6", "v7"):
            for router in range(3):
                body = get(f"/proxy/router-{router}/{version}/sessions/rollout-ready/chat/completions")
                if body.get("component") != "versiond":
                    return False
            if get(f"/proxy/ingress/devshard/{version}/sessions/rollout-public/chat/completions").get("component") != "versiond":
                return False
        return True

    def placement(size):
        def every_backend_healthy():
            for ordinal in range(3):
                pod = f"{args.prefix}-router-{ordinal}"
                def runtime(command):
                    return kube("exec", pod, "-c", "router", "--", "/bin/sh", "-ec",
                                f"printf '%s\\n' '{command}' | socat stdio /var/run/haproxy/haproxy.sock").stdout
                mapping = {fields[1]: fields[2] for line in runtime("show map /etc/haproxy/versions.map").splitlines()
                           if len(fields := line.split()) == 3}
                stats = list(csv.DictReader(io.StringIO(runtime("show stat").removeprefix("# "))))
                for version in ("v6", "v7"):
                    admitted = {row["svname"] for row in stats
                                if row["pxname"] == mapping.get(version) and row["svname"] != "BACKEND"
                                and row["status"].startswith("UP")}
                    if len(admitted) != size:
                        return False
            return True

        # The production coordinator requires reserve, which can deliberately
        # permit a degraded peer. Identical placement requires every test peer
        # healthy on every router; wait for that stronger test precondition.
        wait(f"all {size} backends admitted by every router", every_backend_healthy)
        owners = set()
        paths = [f"/proxy/router-{router}/v6/sessions/coordinated-{session}/chat/completions"
                 for session in range(64) for router in range(3)]
        with ThreadPoolExecutor(max_workers=6) as workers:
            responses = list(workers.map(get, paths))
        for session in range(64):
            results = responses[session * 3:session * 3 + 3]
            assert len({result["owner"] for result in results}) == 1, results
            owners.add(results[0]["owner"])
        expected = {f"{args.prefix}-versiond-{ordinal}" for ordinal in range(size)}
        assert owners == expected, (owners, expected)

    def set_protocols(ordinal, protocols):
        path = "/opt/versiond/gonka-fixture-protocols.json"
        script = "import pathlib,sys; pathlib.Path(sys.argv[1]).write_text(sys.argv[2])"
        kube("exec", f"{args.prefix}-versiond-{ordinal}", "-c", "versiond", "--",
             "python3", "-c", script, path, json.dumps(protocols))

    def ready(ordinal, version=""):
        path = "/readyz" + (f"?version={version}" if version else "")
        return kube("exec", f"{args.prefix}-versiond-{ordinal}", "-c", "versiond", "--",
                    "/bin/busybox", "wget", "-qO", "/dev/null", "-T", "3",
                    "http://127.0.0.1:8080" + path, check=False).returncode == 0

    wait("initial v6/v7 routes", healthy_routes)
    three = values_file("three", baseline)
    four_values = copy.deepcopy(baseline)
    four_values.setdefault("versiond", {})["replicas"] = 4
    four = values_file("four", four_values)
    before = snapshot()
    raw_upgrade(four)
    unchanged(before)
    print("PASS live Helm rejects membership 3 -> 4 before changing any Pod or Service", flush=True)

    print("Testing coordinated maintenance 3 -> 4", flush=True)
    rollout("maintenance-upgrade", four)
    print("PASS maintenance 3 -> 4 reached zero routers and never mixed old/new contracts", flush=True)
    wait("four-member routes", healthy_routes)
    placement(4)
    four_state = snapshot()
    assert f"{args.prefix}-versiond-3" in four_state["pods"]
    for component in ("router", "ingress"):
        for ordinal in range(3):
            pod = f"{args.prefix}-{component}-{ordinal}"
            assert four_state["pods"][pod] != before["pods"][pod], f"old {component} survived membership cutover"
    raw_upgrade(three)
    unchanged(four_state)
    print("PASS live Helm rejects membership 4 -> 3 before deleting any Pod or ordinal Service", flush=True)

    print("Testing coordinated maintenance 4 -> 3", flush=True)
    rollout("maintenance-upgrade", three)
    print("PASS maintenance 4 -> 3 reached zero routers and never mixed old/new contracts", flush=True)
    wait("restored three-member routes", healthy_routes)
    placement(3)
    assert f"{args.prefix}-versiond-3" not in snapshot()["pods"]
    print("PASS explicit maintenance changes both directions and routers agree on the new placement", flush=True)

    candidate_values = copy.deepcopy(baseline)
    candidate_values.setdefault("versiond", {})["extraEnv"] = [
        {"name": "OTEL_RESOURCE_ATTRIBUTES", "value": "gonka.fixture.revision=reserve-test"}]
    candidate = values_file("candidate", candidate_values)
    # Simulate an installation from the previous chart. Helm sends strategy and
    # template in the same StatefulSet patch; the transition must not trigger an
    # uncontrolled legacy RollingUpdate before the coordinator checks reserve.
    kube("patch", "statefulset", f"{args.prefix}-versiond", "--type=merge", "-p",
         json.dumps({"spec": {"updateStrategy": {"type": "RollingUpdate"}}}))
    before = snapshot()
    raw_upgrade(candidate, accepted=True)
    strategy = obj("statefulset", f"{args.prefix}-versiond")["spec"]["updateStrategy"]["type"]
    assert strategy == "OnDelete", strategy
    unchanged(before)
    print("PASS legacy RollingUpdate -> OnDelete plus candidate template does not replace Pods", flush=True)

    for ordinal in (0, 1):
        set_protocols(ordinal, ["v6"])
    set_protocols(2, ["v6", "v7"])

    def last_owner_only():
        if not all(ready(ordinal) and ready(ordinal, "v6") for ordinal in range(3)):
            return False
        if any(ready(ordinal, "v7") for ordinal in (0, 1)) or not ready(2, "v7"):
            return False
        return all(get(f"/proxy/router-{router}/v7/sessions/only-owner/chat/completions")["owner"] ==
                   f"{args.prefix}-versiond-2" for router in range(3))

    wait("coarse-ready fleet with only ordinal2 serving v7", last_owner_only)
    before = snapshot()
    result = rollout("upgrade", candidate, success=False)
    assert "v7" in result.stderr and "refusing to stop" in result.stderr, result.stderr
    unchanged(before)
    assert last_owner_only(), "failed preflight damaged the last live v7 owner"
    print("PASS per-version reserve rejects coarse-ready rollout before deleting the last v7 owner", flush=True)

    for ordinal in (0, 1):
        set_protocols(ordinal, ["v6", "v7"])
    wait("repaired v7 reserve", lambda: all(ready(ordinal, "v7") for ordinal in range(3)))
    # Allow every parent HAProxy's fall/rise checks to observe the repaired route.
    time.sleep(8)
    rollout("upgrade", candidate, resume=True)
    wait("resumed upgrade routes", healthy_routes)
    resumed = snapshot()
    assert all(resumed["pods"][f"{args.prefix}-versiond-{ordinal}"] !=
               before["pods"][f"{args.prefix}-versiond-{ordinal}"] for ordinal in range(3))
    print("PASS unchanged transaction resumes after reserve is repaired", flush=True)

    broken_values = copy.deepcopy(candidate_values)
    broken_values["versiond"]["extraEnv"][0]["value"] = "gonka.fixture.drop_protocol=v7"
    broken = values_file("broken-candidate", broken_values)
    before = snapshot()
    original = obj("pod", f"{args.prefix}-versiond-0")
    print("Testing candidate that stays coarse-ready but loses v7; restoration is required", flush=True)
    result = rollout("upgrade", broken, success=False, timeout=90)
    assert "Restoring previous template" in result.stdout, result.stdout + result.stderr
    assert "Automatic restoration incomplete" not in result.stderr, result.stderr
    wait("restored v7 after failed candidate", healthy_routes)
    restored = obj("pod", f"{args.prefix}-versiond-0")
    assert restored["metadata"]["uid"] != original["metadata"]["uid"]
    old_env = next(c["env"] for c in original["spec"]["containers"] if c["name"] == "versiond")
    new_env = next(c["env"] for c in restored["spec"]["containers"] if c["name"] == "versiond")
    assert old_env == new_env, "candidate restoration did not recover the previous environment"
    after = snapshot()
    for name, uid in before["pods"].items():
        if name != f"{args.prefix}-versiond-0":
            assert after["pods"][name] == uid, f"rollout continued after broken candidate: {name}"
    assert all(ready(ordinal, "v7") for ordinal in range(3))
    print("PASS broken coarse-ready candidate restores previous replica and stops before the next replica", flush=True)
    print("PASS live guarded-rollout smoke (mock applications; no physical node or network-partition test)", flush=True)


if __name__ == "__main__":
    main()
