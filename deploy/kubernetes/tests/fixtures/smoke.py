"""Verify real routers, native sidecar lifecycle and stable Kubernetes routing."""

import argparse
import json
import subprocess
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--prefix", required=True)
    args = parser.parse_args()
    kubectl = ["kubectl", "--kubeconfig", args.kubeconfig, "--context", args.context,
               "--request-timeout=15s", "-n", args.namespace]

    def kube(*command):
        return subprocess.check_output(kubectl + list(command), text=True, timeout=180)

    def get(path):
        with urllib.request.urlopen(args.base_url + path, timeout=15) as response:
            return json.load(response)

    def wait_json(path, predicate, timeout=90):
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            try:
                last = get(path)
                if predicate(last):
                    return last
            except (OSError, ValueError) as error:
                last = repr(error)
            time.sleep(1)
        raise AssertionError(f"timed out waiting for {path}: {last}")

    wait_json("/proxy/ingress/v1/status", lambda body: body.get("component") == "edge-api")
    assert get("/proxy/ingress/v1/inference/test")["component"] == "external"
    assert get("/proxy/ingress/devshard/v6/sessions/public-route/chat/completions")["component"] == "versiond"
    print("PASS public ingress -> policy -> edge, external DAPI, and devshard", flush=True)

    owners = set()
    placements = {}
    for session in range(64):
        responses = [get(f"/proxy/router-{router}/v6/sessions/sticky-{session}/chat/completions") for router in range(3)]
        assert len({response["owner"] for response in responses}) == 1, responses
        assert all(response["ha"] == "true" for response in responses), responses
        owners.add(responses[0]["owner"])
        placements[session] = responses[0]["owner"]
    assert len(owners) == 3, f"all three versiond mock hosts must receive sessions: {owners}"
    print("PASS identical escrow placement through three independent inner routers; HA header cannot be spoofed", flush=True)

    def replace_during_stream(component, path):
        pod = f"{args.prefix}-{component}-2"
        old_uid = json.loads(kube("get", "pod", pod, "-o", "json"))["metadata"]["uid"]
        request = urllib.request.Request(args.base_url + path, data=b"{}", headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=40) as response:
            first = json.loads(response.readline().decode().removeprefix("data: "))
            assert first["event"] == "tick", first
            kube("delete", "pod", pod, "--wait=false")
            events = [first]
            for line in response:
                if line.startswith(b"data: "):
                    events.append(json.loads(line[6:]))
        assert events[-1]["event"] == "complete", f"{component} replacement cut stream: {events}"
        assert len({event["owner"] for event in events}) == 1, events
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            try:
                replacement = json.loads(kube("get", "pod", pod, "-o", "json"))
            except subprocess.CalledProcessError:
                time.sleep(1)
                continue
            ready = any(condition["type"] == "Ready" and condition["status"] == "True"
                        for condition in replacement.get("status", {}).get("conditions", []))
            if replacement["metadata"]["uid"] != old_uid and ready:
                # Pod readiness can precede HAProxy's second successful check.
                time.sleep(5)
                print(f"PASS {component} replacement preserves accepted SSE and replacement becomes ready", flush=True)
                return replacement
            time.sleep(1)
        raise AssertionError(f"{component} replacement did not rejoin")

    replace_during_stream("router", "/proxy/router-2/v6/sessions/router-drain/stream?seconds=12")
    replace_during_stream("ingress", "/proxy/ingress-2/devshard/v6/sessions/ingress-drain/stream?seconds=12")
    host = args.prefix + "-versiond-2"
    escrow = next(session for session, owner in placements.items() if owner == host)
    old_ip = json.loads(kube("get", "pod", host, "-o", "json"))["status"]["podIP"]
    old_vip = json.loads(kube("get", "service", host, "-o", "json"))["spec"]["clusterIP"]
    replacement = replace_during_stream("versiond", f"/proxy/router-0/v6/sessions/sticky-{escrow}/stream?seconds=12")
    new_vip = json.loads(kube("get", "service", host, "-o", "json"))["spec"]["clusterIP"]
    assert new_vip == old_vip, "versiond replacement must retain its hashing address"
    assert replacement["status"]["podIP"] != old_ip, "test must exercise a changed backend Pod IP"
    for session, owner in placements.items():
        responses = [get(f"/proxy/router-{router}/v6/sessions/sticky-{session}/chat/completions") for router in range(3)]
        assert all(response["owner"] == owner for response in responses), responses
    print("PASS stable escrow placement after supervisor PodIP change and router/ingress replacement", flush=True)
    wait_json("/proxy/ingress/devshard/v6/sessions/after-replacement/chat/completions",
              lambda body: body.get("component") == "versiond")
    print("PASS Kubernetes boundary smoke (mock applications; no inference, PostgreSQL, or network-policy enforcement proof)", flush=True)


if __name__ == "__main__":
    main()
