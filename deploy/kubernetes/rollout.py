#!/usr/bin/env python3
"""Guarded HA upgrades, using the operator's kubectl/Helm credentials.

No controller or cluster permissions are added to application pods. A durable
ConfigMap excludes concurrent runs and retains the protected routes and current
replacement for recovery. See README.md before maintenance-upgrade or --resume.
"""
import argparse
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from urllib.parse import quote

import yaml


ROOT = Path(__file__).resolve().parent
COMPONENTS = ("versiond", "router", "ingress")
DEPLOYMENTS = ("edge-api", "oracle")
CONTAINERS = {"versiond": "versiond", "router": "router", "ingress": "proxy-router"}
MAPS = {"router": "/etc/haproxy/versions.map", "ingress": "/etc/haproxy/version-router.map"}


def run(argv, *, data=None, check=True, timeout=60):
    result = subprocess.run(argv, input=data, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{argv[0]} {argv[1]} failed: {result.stderr.strip()}")
    return result


def env(container, key):
    return next((x.get("value", "") for x in container.get("env", []) if x["name"] == key), "")


def container(template, component):
    spec = template["spec"]
    return next(c for c in spec.get("containers", []) + spec.get("initContainers", [])
                if c["name"] == CONTAINERS[component])


def route_map(text):
    result = {}
    for line in text.splitlines():
        fields = line.split()
        if not fields:
            continue
        if len(fields) != 3 or not fields[0].startswith("0x"):
            raise RuntimeError("invalid HAProxy map response")
        result[fields[1]] = fields[2]
    return result


def server_addresses(text):
    columns = None
    result = {}
    for line in text.splitlines():
        fields = line.split()
        if not fields or (columns is None and len(fields) == 1 and fields[0].isdigit()):
            continue
        if fields[0] == "#":
            columns = fields[1:]
            if not {"be_name", "srv_name", "srv_addr"}.issubset(columns):
                raise RuntimeError("HAProxy server state lacks address columns")
        else:
            if columns is None or len(fields) != len(columns):
                raise RuntimeError("invalid HAProxy server state response")
            row = dict(zip(columns, fields))
            result[(row["be_name"], row["srv_name"])] = row["srv_addr"]
    if columns is None:
        raise RuntimeError("missing HAProxy server state header")
    return result


def admitted_addresses(text, backend, addresses):
    lines = text.splitlines()
    if not lines or not lines[0].startswith("# pxname,svname,"):
        raise RuntimeError("invalid HAProxy stats response")
    reader = csv.DictReader(io.StringIO("\n".join([lines[0][2:]] + lines[1:])))
    if not {"pxname", "svname", "status", "addr"}.issubset(reader.fieldnames):
        raise RuntimeError("HAProxy stats lack admission columns")
    # show stat's addr is empty on the supported real image. Join operational
    # admission from stats with resolved IPs from show servers state.
    result = set()
    for row in reader:
        if row["pxname"] != backend or not row["status"].startswith("UP") or row["svname"] == "BACKEND":
            continue
        address = addresses.get((backend, row["svname"]))
        if not address:
            raise RuntimeError("admitted HAProxy server has no resolved address")
        result.add(address)
    return result


def require_reserve(versions, ready, admitted, excluded, minimum):
    """All parents must admit the same healthy reserve, for every route."""
    action = f"refusing to stop {excluded}" if excluded else "insufficient serving reserve"
    for version in sorted(versions):
        reserve = set(ready[version]) - {excluded}
        if len(reserve) < minimum:
            raise RuntimeError(f"{action}: {version} has only "
                               f"{len(reserve)} other ready backends; need {minimum}")
        for parent, routes in admitted.items():
            count = len(reserve & set(routes.get(version, set())))
            if count < minimum:
                raise RuntimeError(f"{action}: {parent} admits only "
                                   f"{count} other backends for {version}; need {minimum}")


class Fleet:
    def __init__(self, args):
        self.args = args
        self.kubectl = [os.environ.get("KUBECTL", "kubectl"), "-n", args.namespace,
                        "--request-timeout=20s"]
        self.helm = [os.environ.get("HELM", "helm"), "--namespace", args.namespace]
        if args.context:
            self.kubectl += ["--context", args.context]
            self.helm += ["--kube-context", args.context]
        if args.kubeconfig:
            self.kubectl += ["--kubeconfig", args.kubeconfig]
            self.helm += ["--kubeconfig", args.kubeconfig]
        self.selector = f"app.kubernetes.io/name=gonka-ha,app.kubernetes.io/instance={args.release}"
        lock_prefix = args.release
        if len(lock_prefix) > 52:
            lock_prefix = lock_prefix[:43] + "-" + hashlib.sha256(args.release.encode()).hexdigest()[:8]
        self.lock_name = lock_prefix + "-ha-rollout"
        self.state = {}
        self.sets = {}
        self.deployment_targets = []
        self.versions = set()

    def kub(self, *argv, **kwargs):
        return run(self.kubectl + list(argv), **kwargs)

    def get(self, kind, name=None):
        argv = ["get", kind]
        argv += [name] if name else ["-l", self.selector]
        return json.loads(self.kub(*argv, "-o", "json").stdout)

    def refresh(self):
        self.sets = {}
        for obj in self.get("statefulsets")["items"]:
            component = obj["spec"]["template"]["metadata"]["labels"].get("app.kubernetes.io/component")
            if component not in COMPONENTS:
                continue
            if component in self.sets:
                raise RuntimeError(f"multiple {component} StatefulSets belong to this release")
            self.sets[component] = obj
        if not set(COMPONENTS).issubset(self.sets):
            raise RuntimeError("expected the installed gonka-ha serving StatefulSets")

    def require_same_names(self, manifests):
        desired = {x["spec"]["template"]["metadata"]["labels"].get("app.kubernetes.io/component"):
                   x["metadata"]["name"] for x in manifests if x["kind"] == "StatefulSet"}
        if any(desired.get(c) != self.sets[c]["metadata"]["name"] for c in COMPONENTS):
            raise RuntimeError("changing serving StatefulSet names is unsupported, including during maintenance; "
                               "keep fullnameOverride unchanged after installation")

    def pods(self, component):
        return sorted([p for p in self.get("pods")["items"]
                       if p["metadata"]["labels"].get("app.kubernetes.io/component") == component],
                      key=lambda p: p["metadata"]["name"])

    def stable(self, component, excluded=""):
        pods = self.pods(component)
        if len(pods) != self.sets[component]["spec"]["replicas"] or not pods:
            raise RuntimeError(f"{component}: wait for every replica")
        for p in pods:
            if p["metadata"]["name"] == excluded:
                continue
            if p["metadata"].get("deletionTimestamp") or not any(
                    c["type"] == "Ready" and c["status"] == "True"
                    for c in p.get("status", {}).get("conditions", [])):
                raise RuntimeError(f"{p['metadata']['name']}: pod is not ready")
        return pods

    def execute(self, pod, component, *argv, check=True):
        return self.kub("exec", pod["metadata"]["name"], "-c", CONTAINERS[component],
                        "--", *argv, check=check)

    def runtime(self, pod, component, command):
        # Commands contain only fixed map paths and keywords, never shell input.
        return self.execute(pod, component, "/bin/sh", "-ec",
                            "printf '%s\\n' '" + command +
                            "' | socat stdio /var/run/haproxy/haproxy.sock").stdout

    def maps(self, pod, component):
        return route_map(self.runtime(pod, component, "show map " + MAPS[component]))

    def ready(self, pod, component, version):
        if pod["metadata"].get("deletionTimestamp"):
            return False
        port = 8080 if component == "versiond" else 8404
        paths = [f"/readyz?version={quote(version, safe='')}"]
        if component == "versiond":
            paths.append(f"/{quote(version, safe='')}/healthz")
        return all(self.execute(pod, component, "/bin/busybox", "wget", "-qO", "/dev/null",
                                "-T", "3", f"http://127.0.0.1:{port}{path}", check=False).returncode == 0
                   for path in paths)

    def discover(self):
        # Published routes remain protected even if an outage makes all of them
        # unready. Also protect a newly serving child before router admission.
        for component in ("router", "ingress"):
            for pod in self.stable(component):
                self.versions.update(self.maps(pod, component))
        for pod in self.stable("versiond"):
            data = self.execute(pod, "versiond", "/bin/busybox", "wget", "-qO-", "-T", "3",
                                "http://127.0.0.1:8080/healthz").stdout
            entries = json.loads(data)
            if not isinstance(entries, list):
                raise RuntimeError("versiond /healthz must expose its child inventory")
            for entry in entries:
                if self.ready(pod, "versiond", entry["name"]):
                    self.versions.add(entry["name"])
        if not self.versions:
            raise RuntimeError("no serving or admitted versions; refusing an unverified upgrade")
        self.state["versions"] = sorted(self.versions)
        self.save()

    def address(self, pod, component):
        if component == "versiond":
            return self.get("service", pod["metadata"]["name"])["spec"]["clusterIP"]
        return pod["status"]["podIP"]

    def view(self, component, excluded=""):
        pods = self.stable(component, excluded)
        peers = [p for p in pods if p["metadata"]["name"] != excluded]
        addresses = {p["metadata"]["name"]: self.address(p, component) for p in peers}
        ready = {v: {p["metadata"]["name"] for p in peers if self.ready(p, component, v)}
                 for v in self.versions}
        parent_component = {"versiond": "router", "router": "ingress"}.get(component)
        admitted = {}
        if parent_component:
            for parent in self.stable(parent_component):
                mapping = self.maps(parent, parent_component)
                stats = self.runtime(parent, parent_component, "show stat")
                servers = server_addresses(self.runtime(parent, parent_component, "show servers state"))
                admitted[parent["metadata"]["name"]] = {
                    v: {name for name, address in addresses.items()
                        if address in admitted_addresses(stats, mapping[v], servers)} if v in mapping else set()
                    for v in self.versions}
        return pods, ready, admitted

    def minimum(self):
        return int(env(container(self.sets["router"]["spec"]["template"], "router"),
                       "VERSIOND_ROUTING_ACTIVATION_MIN_READY"))

    def same_membership(self):
        desired = json.loads(env(container(self.sets["router"]["spec"]["template"], "router"),
                                 "VERSIOND_POOL_ENDPOINTS"))
        for pod in self.pods("router"):
            actual = json.loads(env(container(pod, "router"), "VERSIOND_POOL_ENDPOINTS"))
            if actual != desired:
                raise RuntimeError("running routers have a different placement contract; use maintenance-upgrade")

    def guard(self, component, excluded):
        _, ready, admitted = self.view(component, excluded)
        require_reserve(self.versions, ready, admitted, excluded, self.minimum())

    def wait(self, description, check):
        print(f"Waiting for {description}", flush=True)
        deadline = time.monotonic() + self.args.timeout
        reason = ""
        while time.monotonic() < deadline:
            try:
                if check():
                    return
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                reason = str(error)
            time.sleep(2)
        raise RuntimeError(f"timed out waiting for {description}: {reason}")

    def save(self):
        obj = self.get("configmap", self.lock_name)
        obj["data"] = {"state": json.dumps(self.state)}
        self.kub("replace", "-f", "-", data=json.dumps(obj))

    def acquire(self):
        if self.args.resume:
            self.state = json.loads(self.get("configmap", self.lock_name)["data"]["state"])
            if self.state["mode"] != self.args.command:
                raise RuntimeError("resume must use the original command")
            self.versions = set(self.state["versions"])
            return
        self.state = {"mode": self.args.command, "versions": []}
        self.kub("create", "-f", "-", data=json.dumps({
            "apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": self.lock_name},
            "data": {"state": json.dumps(self.state)}}))

    def scale(self, component, count):
        name = self.sets[component]["metadata"]["name"]
        self.kub("scale", "statefulset", name, f"--replicas={count}")
        self.refresh()

    def stop_tier(self, component):
        print(f"Draining the entire {component} tier", flush=True)
        self.scale(component, 0)
        self.wait(f"all {component} pods to finish graceful shutdown", lambda: not self.pods(component))

    def helm_upgrade(self, maintenance):
        print("Applying Helm values" + (" with routing tiers held offline" if maintenance else ""), flush=True)
        argv = self.helm + ["upgrade", self.args.release, str(ROOT / "charts/gonka-ha"),
                            "--values", self.args.values, "--set", f"maintenance={str(maintenance).lower()}",
                            "--timeout", f"{self.args.timeout}s"]
        # Do not use --atomic: an unattended Helm rollback can change membership
        # or replicas while the coordinator is still draining the old generation.
        run(argv, timeout=self.args.timeout)
        self.refresh()
        # Helm stages OnDelete templates without completing their rollout.
        # Gate the independent Deployment updates before touching serving pods.
        deployments = self.get("deployments")["items"]
        self.deployment_targets = []
        for component in DEPLOYMENTS:
            matches = [d for d in deployments if d["spec"]["template"]["metadata"]["labels"].get(
                "app.kubernetes.io/component") == component]
            if len(matches) != 1:
                raise RuntimeError(f"expected exactly one {component} Deployment")
            self.deployment_targets.append(matches[0]["metadata"])
        self.wait("edge-api and oracle Deployment rollouts", self.deployments_ready)

    def deployments_ready(self):
        for target in self.deployment_targets:
            name = target["name"]
            obj = self.get("deployment", name)
            metadata, spec, status = obj["metadata"], obj["spec"], obj.get("status", {})
            if (metadata["uid"] != target["uid"] or metadata["generation"] != target["generation"] or
                    metadata.get("deletionTimestamp")):
                raise RuntimeError(f"Deployment {name} changed concurrently")
            desired = spec.get("replicas", 1)
            # Availability alone can describe old replicas while a bad new
            # image is stuck. Require the entire observed generation to finish.
            if (spec.get("paused") or status.get("observedGeneration", 0) < target["generation"] or
                    any(status.get(key, 0) != desired for key in
                        ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas"))):
                raise RuntimeError(f"Deployment {name} rollout incomplete: desired={desired}, "
                                   f"status={json.dumps(status, sort_keys=True)}")
        return True

    def delete(self, pod):
        name = pod["metadata"]["name"]
        uri = f"/api/v1/namespaces/{quote(self.args.namespace, safe='')}/pods/{quote(name, safe='')}"
        self.kub("delete", "--raw", uri, "-f", "-", data=json.dumps({
            "apiVersion": "v1", "kind": "DeleteOptions",
            "preconditions": {"uid": pod["metadata"]["uid"]}}))

    def recovered(self, component, name, old_uid, revision):
        pods, ready, admitted = self.view(component)
        candidate = next((p for p in pods if p["metadata"]["name"] == name), None)
        if not candidate or candidate["metadata"]["uid"] == old_uid:
            return False
        if candidate["metadata"]["labels"].get("controller-revision-hash") != revision:
            raise RuntimeError(f"{name}: unexpected replacement ControllerRevision")
        missing = [v for v in sorted(self.versions) if name not in ready[v] or
                   any(name not in routes[v] for routes in admitted.values())]
        if missing:
            raise RuntimeError(f"{name}: readiness or parent admission missing for {', '.join(missing)}")
        return True

    def restore(self):
        current = self.state.get("replacement")
        if not current:
            return
        component, name = current["component"], current["pod"]
        sts = self.sets[component]["metadata"]["name"]
        print(f"Restoring previous template for {name}", flush=True)
        live = self.get("statefulset", sts)
        if live["spec"]["template"] != current["template"]:
            if (live["metadata"]["generation"] != current["generation"] or
                    live["spec"]["template"] != current["target_template"]):
                raise RuntimeError("StatefulSet changed concurrently; refusing to overwrite its template")
            self.kub("patch", "statefulset", sts, "--type=json", "-p",
                     json.dumps([{"op": "test", "path": "/metadata/generation", "value": current["generation"]},
                                 {"op": "replace", "path": "/spec/template", "value": current["template"]}]))
        self.refresh()
        self.wait("restored StatefulSet template", self.observed)
        pods = self.pods(component)
        pod = next((p for p in pods if p["metadata"]["name"] == name), None)
        if pod:
            if pod["metadata"]["uid"] == current["uid"] and not pod["metadata"].get("deletionTimestamp"):
                # Interrupted before DELETE: the original generation is still
                # present. Restoring its template is sufficient.
                del self.state["replacement"]
                self.save()
                return
            if (pod["metadata"]["labels"].get("controller-revision-hash") == current["previous_revision"] and
                    not pod["metadata"].get("deletionTimestamp")):
                # Recovery may have completed after a timeout/interruption.
                # Prove that restored generation instead of draining it twice.
                self.wait(f"already restored {name}", lambda: self.recovered(
                    component, name, current["uid"], current["previous_revision"]))
                del self.state["replacement"]
                self.save()
                return
            # A failed candidate cannot authorize deleting a last surviving
            # route. Leave the journal in place if the remaining fleet degraded.
            if pod["metadata"]["uid"] != current["uid"]:
                self.guard(component, name)
            uid = pod["metadata"]["uid"]
            if not pod["metadata"].get("deletionTimestamp"):
                self.delete(pod)
        else:
            uid = current["uid"]
        self.wait(f"restored {name} and parent admission",
                  lambda: self.recovered(component, name, uid, current["previous_revision"]))
        del self.state["replacement"]
        self.save()

    def replace_component(self, component, offline=False):
        self.refresh()
        sts = self.sets[component]
        target = sts.get("status", {}).get("updateRevision")
        if not target:
            raise RuntimeError(f"{component}: no target ControllerRevision")
        # Ascending order is intentional, but safety must not depend on order.
        for pod in self.pods(component):
            name = pod["metadata"]["name"]
            # Scale-down pods can outlive Helm apply while versiond drains.
            # They have no replacement; leave their deletion to the controller.
            if (pod["metadata"].get("deletionTimestamp") or
                    int(name.rsplit("-", 1)[1]) >= sts["spec"].get("replicas", 1)):
                continue
            if pod["metadata"]["labels"].get("controller-revision-hash") == target:
                continue
            if not offline:
                self.discover()
                self.guard(component, name)
            revision = self.get("controllerrevision", pod["metadata"]["labels"]["controller-revision-hash"])
            template = revision["data"]["spec"]["template"]
            template.pop("$patch", None)
            self.state["replacement"] = {"component": component, "pod": name,
                                          "uid": pod["metadata"]["uid"], "template": template,
                                          "generation": sts["metadata"]["generation"],
                                          "target_template": sts["spec"]["template"],
                                          "previous_revision": pod["metadata"]["labels"]["controller-revision-hash"]}
            self.save()
            live = self.get("statefulset", sts["metadata"]["name"])
            if live["metadata"]["generation"] != sts["metadata"]["generation"]:
                # DELETE has not happened; do not undo another operator's change.
                del self.state["replacement"]
                self.save()
                raise RuntimeError("StatefulSet changed concurrently; refusing pod deletion")
            print(f"Replacing {name}; protected versions: {', '.join(sorted(self.versions))}", flush=True)
            self.delete(pod)
            if offline:
                def converged():
                    return any(p["metadata"]["name"] == name and p["metadata"]["uid"] != pod["metadata"]["uid"] and
                               p["metadata"]["labels"].get("controller-revision-hash") == target
                               for p in self.stable(component))
            else:
                def converged():
                    return self.recovered(component, name, pod["metadata"]["uid"], target)
            self.wait(f"{name} and every protected route", converged)
            del self.state["replacement"]
            self.save()

    def run(self):
        # Check schema/templates before any drain or lock acquisition. The real
        # upgrade also runs the chart's live-cluster lookup guards.
        rendered = run(self.helm + ["template", self.args.release, str(ROOT / "charts/gonka-ha"),
                                    "--values", self.args.values]).stdout
        manifests = [x for x in yaml.safe_load_all(rendered) if x]
        oracle = next(x for x in manifests if x["kind"] == "Deployment" and
                      x["spec"]["template"]["metadata"]["labels"].get("app.kubernetes.io/component") == "oracle")
        allowed = set(env(oracle["spec"]["template"]["spec"]["containers"][0], "ORACLE_ALLOW").split())
        self.refresh()
        self.require_same_names(manifests)
        self.acquire()
        try:
            if self.args.command == "upgrade":
                if self.state.get("replacement"):
                    self.restore()
                self.same_membership()
                self.discover()
                self.require_protocols(allowed)
                self.helm_upgrade(False)
                self.same_membership()
                # Wait for controller observation before selecting revisions.
                self.wait("StatefulSet revisions", self.observed)
                for component in COMPONENTS:
                    self.replace_component(component)
            else:
                if not self.versions:
                    self.discover()
                self.require_protocols(allowed)
                # No new placement exists until ALL old routers, including
                # terminating pods and their accepted streams, have gone.
                self.stop_tier("ingress")
                self.stop_tier("router")
                self.helm_upgrade(True)
                self.wait("StatefulSet revisions", self.observed)
                self.replace_component("versiond", offline=True)
                values = json.loads(run(self.helm + ["get", "values", self.args.release,
                                                     "--all", "-o", "json"]).stdout)
                self.scale("router", values["router"]["replicas"])
                self.wait("new routers and all protected versions", self.maintenance_ready)
                self.helm_upgrade(False)
                self.wait("public ingress and all protected versions", self.all_ready)
            self.wait("edge-api and oracle Deployment rollouts", self.deployments_ready)
            self.kub("delete", "configmap", self.lock_name)
        except BaseException:
            if self.args.command == "upgrade" and self.state.get("replacement"):
                try:
                    self.restore()
                except Exception as error:
                    print(f"Automatic restoration incomplete: {error}", file=sys.stderr)
            print(f"Stopped. Journal retained in ConfigMap {self.lock_name}. "
                  "After resolving the failure and ensuring no coordinator is running, "
                  "repeat the same command with --resume. Maintenance may leave ingress OFF.", file=sys.stderr)
            raise

    def observed(self):
        self.refresh()
        return all(s.get("status", {}).get("observedGeneration", 0) >= s["metadata"]["generation"]
                   for s in self.sets.values())

    def require_protocols(self, allowed):
        missing = self.versions - allowed
        if missing:
            raise RuntimeError("refusing to retire protected protocols: " + ", ".join(sorted(missing)))

    def maintenance_ready(self):
        self.refresh()
        # Parent ingress is intentionally absent. Every router must admit a
        # reserve for every previously served protocol before reopening it.
        pods, ready, admitted = self.view("versiond")
        require_reserve(self.versions, ready, admitted, "", self.minimum())
        return all(self.ready(p, "router", v) for p in self.stable("router") for v in self.versions)

    def all_ready(self):
        self.refresh()
        for component in COMPONENTS:
            _, ready, admitted = self.view(component)
            require_reserve(self.versions, ready, admitted, "", self.minimum())
        return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["upgrade", "maintenance-upgrade"])
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--release", required=True)
    parser.add_argument("--values", required=True, help="complete saved Helm values file")
    parser.add_argument("--context")
    parser.add_argument("--kubeconfig")
    parser.add_argument("--timeout", type=int, default=2700, help="seconds per drain/recovery phase")
    parser.add_argument("--resume", action="store_true", help="resume retained journal; first verify no other run is active")
    args = parser.parse_args()
    if args.timeout < 1:
        parser.error("timeout must be positive")
    Fleet(args).run()


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        print(f"HA rollout failed: {error}", file=sys.stderr)
        sys.exit(1)
