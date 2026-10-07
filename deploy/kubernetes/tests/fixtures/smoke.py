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
        print(f"PASS {component} accepted SSE completed during deletion; waiting for replacement", flush=True)
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

    # Peer RPC (devshard phase 6): peers dial {InferenceUrl.host}:9443 with
    # HTTP/2 over TLS. The hop is ingress proxy-router :9443 -> router :8081
    # (proto h2) -> versiond. It must share escrow placement with the JSON hop,
    # and the JSON hop must refuse /rpc/. curl runs inside a router pod: the
    # Python client here has no HTTP/2.
    ingress_host = f"{args.prefix}-ingress.{args.namespace}.svc.cluster.local"
    rpc_path = "/v6/sessions/{escrow}/rpc/devshard.transport.v1.SessionService/Chat"

    def curl(*argv):
        result = subprocess.run(
            kubectl + ["exec", f"{args.prefix}-router-0", "-c", "router", "--", "curl", "-sS", "-o", "/dev/stderr",
                       "-w", "%{http_version} %{http_code}", "--max-time", "15", *argv],
            text=True, capture_output=True, timeout=60)
        return result.returncode, result.stdout.strip(), result.stderr

    def peer_rpc(escrow):
        deadline = time.monotonic() + 90
        while True:
            code, status, body = curl("-k", "--http2", "-X", "POST", "--data", "{}",
                                      f"https://{ingress_host}:9443" + rpc_path.format(escrow=escrow))
            if code == 0 and status == "2 200":
                return json.loads(body)
            if time.monotonic() > deadline:
                raise AssertionError(f"peer RPC hop did not answer over HTTP/2: exit={code} status={status!r} body={body!r}")
            time.sleep(1)

    first = peer_rpc("peer-rpc-smoke")
    assert first["component"] == "versiond", first
    assert first["path"] == rpc_path.format(escrow="peer-rpc-smoke"), first
    for session in list(placements)[:8]:
        assert peer_rpc(f"sticky-{session}")["owner"] == placements[session], session
    print("PASS peer RPC :9443 is HTTP/2 over TLS end to end and shares escrow placement with the JSON hop", flush=True)

    code, status, _ = curl("--http2-prior-knowledge", "-X", "POST",
                           f"http://{ingress_host}:9443" + rpc_path.format(escrow="cleartext"))
    assert code != 0 or not status.endswith(" 200"), f"cleartext HTTP/2 must not be served on :9443 with tlsSecret: {status}"
    code, status, _ = curl("-X", "POST", f"http://{ingress_host}:80/devshard" + rpc_path.format(escrow="json-hop"))
    assert status.endswith(" 404"), f"/rpc/ must be refused on the JSON hop: exit={code} status={status!r}"
    print("PASS peer RPC is refused on the JSON hop and on cleartext :9443", flush=True)
    print("PASS Kubernetes boundary smoke (mock applications; no inference, PostgreSQL, or network-policy enforcement proof)", flush=True)


if __name__ == "__main__":
    main()
