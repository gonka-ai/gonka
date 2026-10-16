#!/usr/bin/env python3
"""Prove every Kubernetes HA child shares an independently selected primary.

Uses loopback-only versiond proof endpoints through kubectl exec. The reference
psql connection uses the operator's PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD
and TLS environment, never credentials read from the workload being checked.
Run one deployment/storage verification at a time for this database.
"""
import argparse
import json
import os
import subprocess
import sys
import uuid


def run(args, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, timeout=60, **kwargs)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def validate_proof(proof, identity):
    targets = proof.get("targets", [])
    if (proof.get("identity") != identity or not proof.get("snapshot") or
            not targets or proof.get("children") != len(targets) or
            any(not target.get("generation") or not target.get("version") for target in targets) or
            len({target["generation"] for target in targets}) != len(targets)):
        raise RuntimeError("incomplete proof or database identity mismatch")


def verify(pods, reference_identity, get_proof, challenge, reference_read):
    proofs = {}
    for pod in pods:
        proofs[pod] = get_proof(pod)
        validate_proof(proofs[pod], reference_identity)
    count = 0
    for writer, proof in proofs.items():
        for target in proof["targets"]:
            nonce = str(uuid.uuid4())
            for pod, candidate, child, operation in [
                (writer, proof, target, "write"),
                *((p, candidate, child, "read") for p, candidate in proofs.items()
                  for child in candidate["targets"]),
            ]:
                response = challenge(pod, {
                    "operation": operation, "nonce": nonce,
                    "snapshot": candidate["snapshot"], "generation": child["generation"],
                })
                if (response.get("identity") != reference_identity or
                        response.get("snapshot") != candidate["snapshot"] or
                        response.get("generation") != child["generation"] or
                        response.get("found") is not True):
                    raise RuntimeError(f"{pod}: storage challenge failed; database or generation changed")
            if reference_read() != f"{reference_identity}|{nonce}":
                raise RuntimeError("reference primary did not observe the write (or another check is running)")
            count += 1
    for pod, proof in proofs.items():
        current = get_proof(pod)
        validate_proof(current, reference_identity)
        if current["snapshot"] != proof["snapshot"]:
            raise RuntimeError(f"{pod}: child generations changed during verification; retry when stable")
    return count


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--release", required=True)
    parser.add_argument("--context", help="kubectl context (defaults to selected context)")
    parser.add_argument("--kubeconfig")
    args = parser.parse_args()
    for key in ("PGHOST", "PGDATABASE", "PGUSER"):
        if not os.environ.get(key):
            parser.error(f"set {key} explicitly for the independent reference primary")
    kubectl = ["kubectl", "--namespace", args.namespace]
    if args.context:
        kubectl += ["--context", args.context]
    if args.kubeconfig:
        kubectl += ["--kubeconfig", args.kubeconfig]
    selector = f"app.kubernetes.io/name=gonka-ha,app.kubernetes.io/instance={args.release},app.kubernetes.io/component=versiond"

    def inventory():
        sets = json.loads(run(kubectl + ["get", "statefulsets", "-l", selector, "-o", "json"]))["items"]
        if len(sets) != 1:
            raise RuntimeError("expected exactly one versiond StatefulSet")
        replicas = sets[0]["spec"]["replicas"]
        items = json.loads(run(kubectl + ["get", "pods", "-l", selector, "-o", "json"]))["items"]
        if len(items) != replicas or replicas < 2:
            raise RuntimeError("wait for every desired versiond replica before checking storage")
        result = {}
        for item in items:
            statuses = item.get("status", {}).get("containerStatuses", [])
            versiond = next((x for x in statuses if x["name"] == "versiond"), {})
            if (item["metadata"].get("deletionTimestamp") or not versiond.get("ready") or
                    not versiond.get("containerID")):
                raise RuntimeError("versiond pods must be ready and not terminating")
            result[item["metadata"]["name"]] = (item["metadata"]["uid"], versiond["containerID"])
        return result

    def http(pod, path, payload=None):
        command = kubectl + ["exec", pod, "-c", "versiond", "--", "/bin/busybox", "wget", "-qO-", "-T", "10"]
        if payload is not None:
            command += ["--header", "Content-Type: application/json", "--post-data", json.dumps(payload)]
        return json.loads(run(command + ["http://127.0.0.1:8080" + path]))

    def reference(sql):
        environment = {**os.environ, "PGCONNECT_TIMEOUT": "5", "PGOPTIONS": "-c statement_timeout=10000"}
        return run(["psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", sql], env=environment)

    identity = reference("SELECT identity::text FROM devshard_storage_identity WHERE singleton AND NOT pg_is_in_recovery()")
    if not identity or "\n" in identity:
        raise RuntimeError("reference must be an initialized writable primary")
    before = inventory()
    count = verify(before, identity,
                   lambda pod: http(pod, "/internal/storage-identity"),
                   lambda pod, payload: http(pod, "/internal/storage-challenge", payload),
                   lambda: reference("SELECT identity::text || '|' || COALESCE(challenge::text, '') FROM devshard_storage_identity WHERE singleton AND NOT pg_is_in_recovery()"))
    if inventory() != before:
        raise RuntimeError("pods changed during verification; retry when stable")
    print(f"Storage verified: {count} HA generations across {len(before)} pods share the reference primary.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        print(f"Storage verification failed: {error}", file=sys.stderr)
        sys.exit(1)
