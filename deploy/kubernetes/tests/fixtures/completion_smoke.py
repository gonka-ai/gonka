"""Live regressions for Deployment completion and immutable serving names."""
import argparse
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser()
    for option in ("kubeconfig", "context", "namespace", "values", "release", "helm"):
        parser.add_argument("--" + option, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    sys.path.insert(0, str(root))
    from rollout import Fleet

    kubectl = ["kubectl", "--kubeconfig", args.kubeconfig, "--context", args.context,
               "--request-timeout=15s", "-n", args.namespace]
    helm = [args.helm, "--kubeconfig", args.kubeconfig, "--kube-context", args.context,
            "--namespace", args.namespace]
    coordinator = [sys.executable, str(root / "rollout.py")]
    flags = []
    for key in ("kubeconfig", "context", "namespace", "release"):
        flags += ["--" + key, getattr(args, key)]
    baseline = json.loads(Path(args.values).read_text())
    selector = f"app.kubernetes.io/name=gonka-ha,app.kubernetes.io/instance={args.release}"

    def run(command, *, check=True, timeout=180):
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout,
                                env={**os.environ, "HELM": args.helm})
        if check and result.returncode:
            raise AssertionError(result.stdout + result.stderr)
        return result

    def kube(*command):
        return run(kubectl + list(command))

    def objects(kind):
        return json.loads(kube("get", kind, "-l", selector, "-o", "json").stdout)["items"]

    def wait(description, predicate):
        deadline = time.monotonic() + 180
        reason = ""
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except RuntimeError as error:
                reason = str(error)
            time.sleep(2)
        raise AssertionError(f"timed out waiting for {description}: {reason}")

    def serving_snapshot():
        return {p["metadata"]["name"]: (p["metadata"]["uid"], p["metadata"].get("deletionTimestamp"))
                for p in objects("pods") if p["metadata"]["labels"].get("app.kubernetes.io/component")
                in ("versiond", "router", "ingress")}

    def values_file(label, values):
        path = Path(args.values).with_name(f"completion-{label}.json")
        path.write_text(json.dumps(values))
        return str(path)

    def journal_exists():
        return run(kubectl + ["get", "configmap", args.release + "-ha-rollout"], check=False).returncode == 0

    probe = Fleet(argparse.Namespace(**{**vars(args), "timeout": 180, "resume": False, "command": "upgrade"}))
    probe.versions = set(baseline["protocols"])
    wait("initial per-version reserve and admission", probe.all_ready)
    before = serving_snapshot()

    renamed = copy.deepcopy(baseline)
    renamed["fullnameOverride"] = "renamed-ha"
    rename_values = values_file("rename", renamed)
    result = run(coordinator + ["maintenance-upgrade", *flags, "--values", rename_values,
                                "--timeout", "30"], check=False)
    assert result.returncode != 0 and "StatefulSet names is unsupported" in result.stderr, result.stderr
    assert not journal_exists(), "rename preflight acquired a journal"
    assert serving_snapshot() == before, "rename preflight drained serving pods"
    print("PASS: maintenance rename refused before lock or drain", flush=True)

    for key, component in (("edgeApi", "edge-api"), ("oracle", "oracle")):
        broken = copy.deepcopy(baseline)
        # The Never policy makes this deterministic without any registry/network.
        broken["images"][key] = "gonka-fixture-missing:does-not-exist"
        path = values_file(component, broken)
        result = run(coordinator + ["upgrade", *flags, "--values", path, "--timeout", "20"], check=False)
        assert result.returncode != 0, "bad Deployment image returned success"
        assert "Deployment rollouts" in result.stderr and "rollout incomplete" in result.stderr, result.stderr
        assert journal_exists(), "failed Deployment update lost its recovery journal"
        assert serving_snapshot() == before, "Deployment failure replaced serving pods"
        pods = [p for p in objects("pods") if p["metadata"]["labels"].get("app.kubernetes.io/component") == component]
        assert any(s.get("state", {}).get("waiting", {}).get("reason") == "ErrImageNeverPull"
                   for p in pods for s in p.get("status", {}).get("containerStatuses", [])), pods
        run(coordinator + ["upgrade", *flags, "--values", args.values, "--resume", "--timeout", "120"], timeout=300)
        assert not journal_exists(), "successful corrected resume retained journal"
        for deployment in objects("deployments"):
            if deployment["spec"]["template"]["metadata"]["labels"].get("app.kubernetes.io/component") not in ("edge-api", "oracle"):
                continue
            status = deployment["status"]
            assert status["observedGeneration"] >= deployment["metadata"]["generation"]
            assert all(status.get(k, 0) == deployment["spec"]["replicas"]
                       for k in ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")), status
        print(f"PASS: bad {component} image fails with journal; corrected --resume completes", flush=True)

    # Exercise Helm's real API lookup after routing tiers have fully drained.
    probe.refresh()
    for component in ("ingress", "router"):
        probe.stop_tier(component)
    result = run(helm + ["upgrade", args.release, str(root / "charts/gonka-ha"),
                        "--values", rename_values, "--set", "maintenance=true"], check=False)
    assert result.returncode != 0 and "StatefulSet names is unsupported" in result.stderr, result.stderr
    assert all(not s["metadata"]["name"].startswith("renamed-ha") for s in objects("statefulsets"))
    # Restore the same names for subsequent smoke suites, without replacing the
    # existing versiond pods or changing placement.
    run(helm + ["upgrade", args.release, str(root / "charts/gonka-ha"), "--values", args.values])
    wait("restored serving tiers", probe.all_ready)
    print("PASS: Helm rejects offline rename; original names reopen successfully", flush=True)


if __name__ == "__main__":
    main()
