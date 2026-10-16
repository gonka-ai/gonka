#!/usr/bin/env python3
"""Validate rendered chart variants against checksum-pinned CNPG 1.28.0 CRDs.

Requires Helm 3, PyYAML and jsonschema. No Kubernetes cluster or registry pulls.
--crd-dir reads pre-downloaded CRDs instead of downloading them from upstream.
"""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import urllib.request

import jsonschema
import yaml


CHART = Path(__file__).resolve().parents[1]
UPSTREAM = "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v1.28.0/config/crd/bases/"
CRDS = {
    "Cluster": (
        "postgresql.cnpg.io_clusters.yaml",
        "51268ebddd087e77d91a0251da74176bae9814b6009b33efcf552b965e66b44c",
    ),
    "ScheduledBackup": (
        "postgresql.cnpg.io_scheduledbackups.yaml",
        "4c3c0747daae3c8664d7d4695b3f62f8fa921b87c28d7ce3169bdeb2411d5141",
    ),
}


def reject_unknown_fields(schema):
    """Also catch fields Kubernetes would silently prune from a CRD resource."""
    if not isinstance(schema, dict) or schema.get("x-kubernetes-preserve-unknown-fields"):
        return
    if schema.get("type") == "object" and "properties" in schema:
        schema.setdefault("additionalProperties", False)
    for name, child in schema.get("properties", {}).items():
        if name != "metadata":  # Kubernetes supplies ObjectMeta separately.
            reject_unknown_fields(child)
    for keyword in ("items", "additionalProperties"):
        reject_unknown_fields(schema.get(keyword))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--crd-dir", type=Path)
    args = parser.parse_args()
    validators = {}
    for kind, (filename, digest) in CRDS.items():
        if args.crd_dir:
            content = (args.crd_dir / filename).read_bytes()
        else:
            with urllib.request.urlopen(UPSTREAM + filename, timeout=30) as response:
                content = response.read()
        assert hashlib.sha256(content).hexdigest() == digest, filename + " SHA256 mismatch"
        crd = yaml.safe_load(content)
        version = next(v for v in crd["spec"]["versions"] if v["name"] == "v1")
        schema = copy.deepcopy(version["schema"]["openAPIV3Schema"])
        reject_unknown_fields(schema)
        validators[kind] = jsonschema.Draft7Validator(schema)

    values = ["-f", str(CHART / "tests/render-values.yaml")]
    subprocess.run([args.helm, "lint", str(CHART), "--strict", *values], check=True)

    def render(overrides=None, apis=("postgresql.cnpg.io/v1",), error=None):
        with tempfile.TemporaryDirectory(prefix="gonka-pg-chart-") as work:
            path = Path(work) / "override.json"
            path.write_text(json.dumps(overrides or {}))
            command = [args.helm, "template", "db", str(CHART), "--namespace", "gonka",
                       *values, "-f", str(path)]
            for api in apis:
                command += ["--api-versions", api]
            result = subprocess.run(command, text=True, capture_output=True)
        if error:
            assert result.returncode != 0, f"expected failure: {error}"
            assert error in result.stderr, result.stderr
            return []
        assert result.returncode == 0, result.stderr
        docs = [d for d in yaml.safe_load_all(result.stdout) if d]
        for resource in docs:
            errors = sorted(validators[resource["kind"]].iter_errors(resource), key=str)
            assert not errors, "\n".join(str(e) for e in errors)
        return docs

    cluster, = render()
    assert cluster["metadata"]["name"] == "db-gonka-postgres"
    assert cluster["metadata"]["annotations"]["helm.sh/resource-policy"] == "keep"
    assert cluster["spec"]["instances"] == 3
    assert cluster["spec"]["postgresql"]["synchronous"] == {
        "method": "any", "number": 1, "dataDurability": "required", "failoverQuorum": True,
    }
    assert cluster["spec"]["postgresql"]["parameters"]["synchronous_commit"] == "on"
    assert cluster["spec"]["affinity"]["podAntiAffinityType"] == "required"
    assert cluster["spec"]["affinity"]["topologyKey"] == "kubernetes.io/hostname"
    assert cluster["spec"]["storage"]["pvcTemplate"]["accessModes"] == ["ReadWriteOnce"]
    assert "backup" not in cluster["spec"]

    full_apis = ("postgresql.cnpg.io/v1", "snapshot.storage.k8s.io/v1")
    docs = render({
        "snapshots": {"enabled": True, "className": "retain-snapshots", "immediate": True},
        "nodeSelector": {"workload": "postgres"},
        "tolerations": [{"key": "postgres", "operator": "Exists", "effect": "NoSchedule"}],
        "storage": {"storageClass": "fast-disks", "size": "200Gi"},
        "imagePullSecrets": [{"name": "registry-credentials"}],
    }, apis=full_apis)
    cluster = next(d for d in docs if d["kind"] == "Cluster")
    backup = next(d for d in docs if d["kind"] == "ScheduledBackup")
    assert cluster["spec"]["backup"]["volumeSnapshot"]["snapshotOwnerReference"] == "none"
    assert backup["spec"]["backupOwnerReference"] == "none"
    assert backup["spec"]["method"] == "volumeSnapshot"
    assert backup["spec"]["online"] is False
    assert backup["spec"]["cluster"]["name"] == cluster["metadata"]["name"]

    restored, = render({"recovery": {"volumeSnapshot": "known-cold-snapshot"}}, apis=full_apis)
    bootstrap = restored["spec"]["bootstrap"]
    assert "initdb" not in bootstrap
    assert bootstrap["recovery"]["volumeSnapshots"]["storage"]["name"] == "known-cold-snapshot"
    assert bootstrap["recovery"]["secret"]["name"] == "gonka-postgres-credentials"

    render(apis=(), error="CloudNativePG >= 1.28.0")
    render({"instances": 1}, error="instances")
    render({"imageName": "postgres:17.5"}, error="imageName")
    render({"imageName": "postgres:16"}, error="imageName")
    render({"credentialsSecret": ""}, error="credentialsSecret")
    render({"snapshots": {"enabled": True, "className": "snapshots"}}, error="snapshot.storage.k8s.io/v1")
    render({"snapshots": {"enabled": True}}, apis=full_apis, error="snapshots.className")
    render({"snapshots": {"schedule": "0 2 * * *"}}, error="schedule")
    render({"recovery": {"volumeSnapshot": "known-cold-snapshot"}}, error="snapshot.storage.k8s.io/v1")
    render({"postgresql": {"parameters": {"synchronous_commit": "off"}}}, error="postgresql")
    print("gonka-postgres: lint, 3 variants, CNPG 1.28.0 schemas and 10 rejection cases passed")


if __name__ == "__main__":
    main()
