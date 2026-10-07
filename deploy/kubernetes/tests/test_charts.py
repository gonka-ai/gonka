"""Deployment contract tests. Run with HELM=/path/to/helm python3 -m unittest discover -s deploy/kubernetes/tests."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/gonka-ha"
HELM = os.environ.get("HELM", "helm")


class UniqueKeyLoader(yaml.SafeLoader):
    pass


def unique_mapping(loader, node):
    pairs = loader.construct_pairs(node)
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate YAML key: {key}")
        result[key] = value
    return result


UniqueKeyLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, unique_mapping)


def render(overrides=None, kube="1.33.0"):
    with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml") as values:
        yaml.safe_dump(overrides or {}, values)
        values.flush()
        return subprocess.run(
            [HELM, "template", "test", str(CHART), "--namespace", "gonka",
             "--kube-version", kube, "-f", str(ROOT / "examples/values.yaml"),
             "-f", values.name], text=True, capture_output=True,
        )


def env(container):
    return {item["name"]: item.get("value", item.get("valueFrom")) for item in container["env"]}


class ChartContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        result = render()
        if result.returncode:
            raise AssertionError(result.stderr)
        cls.docs = [x for x in yaml.load_all(result.stdout, Loader=UniqueKeyLoader) if x]
        cls.objects = {(x["kind"], x["metadata"]["name"]): x for x in cls.docs}

    def pod(self, component, kind="StatefulSet"):
        return self.objects[(kind, f"test-gonka-ha-{component}")]["spec"]["template"]["spec"]

    def test_routes_have_one_stable_address_per_supervisor(self):
        router = env(self.pod("router")["containers"][0])
        self.assertEqual(router["VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS"], "true")
        endpoints = json.loads(router["VERSIOND_POOL_ENDPOINTS"])
        self.assertEqual(len(endpoints), 3)
        for index, endpoint in enumerate(endpoints):
            name = f"test-gonka-ha-versiond-{index}"
            self.assertEqual(endpoint["host"], f"{name}.gonka.svc.cluster.local")
            service = self.objects[("Service", name)]["spec"]
            self.assertEqual(service["selector"]["statefulset.kubernetes.io/pod-name"], name)
            self.assertTrue(service["publishNotReadyAddresses"])
            self.assertNotEqual(service.get("clusterIP"), "None")

    def test_ha_storage_and_identity_are_mandatory(self):
        values = env(self.pod("versiond")["containers"][0])
        self.assertEqual(values["GONKA_HA"], "true")
        self.assertEqual(values["DEVSHARD_STORAGE_MODE"], "postgres")
        self.assertEqual(values["VERSIOND_NON_HA_VERSIONS"], "")
        self.assertIn("secretKeyRef", values["PGPASSWORD"])
        self.assertIn("secretKeyRef", values["KEYRING_PASSWORD"])
        for component in ["versiond", "router", "ingress"]:
            self.assertTrue(self.objects[("StatefulSet", f"test-gonka-ha-{component}")]["spec"]["volumeClaimTemplates"])

    def test_same_filtered_governance_catalog(self):
        ingress = env(self.pod("ingress")["initContainers"][0])
        router = env(self.pod("router")["containers"][0])
        supervisor = env(self.pod("versiond")["containers"][0])
        self.assertEqual(ingress["VERSIOND_ROUTING_CATALOG_URL"], router["VERSIOND_ROUTING_CATALOG_URL"])
        self.assertEqual(supervisor["VERSIOND_ORACLE_URL"], router["VERSIOND_ROUTING_CATALOG_URL"])
        self.assertIn("-oracle.gonka.svc.cluster.local", supervisor["VERSIOND_ORACLE_URL"])

    def test_chain_grpc_tls_reaches_supervisor_and_query_api(self):
        for enabled in (False, True):
            with self.subTest(enabled=enabled):
                result = render({"external": {"chainGrpcTls": enabled}})
                self.assertEqual(result.returncode, 0, result.stderr)
                objects = {item["metadata"]["name"]: item for item in
                           yaml.load_all(result.stdout, Loader=UniqueKeyLoader)
                           if item and item["kind"] in ("StatefulSet", "Deployment")}
                for component in ("versiond", "edge-api"):
                    pod = objects[f"test-gonka-ha-{component}"]["spec"]["template"]["spec"]
                    self.assertEqual(env(pod["containers"][0])["CHAIN_GRPC_TLS"], str(enabled).lower())

    def test_explicit_plaintext_postgres_reuses_existing_private_endpoint(self):
        for mode in ("disable", "require"):
            with self.subTest(mode=mode):
                result = render({"postgres": {"host": "compose-db.private", "sslMode": mode, "caSecret": ""}})
                self.assertEqual(result.returncode, 0, result.stderr)
                supervisor = next(item for item in yaml.load_all(result.stdout, Loader=UniqueKeyLoader)
                                  if item and item["kind"] == "StatefulSet" and
                                  item["metadata"]["name"] == "test-gonka-ha-versiond")
                pod = supervisor["spec"]["template"]["spec"]
                values = env(pod["containers"][0])
                self.assertEqual(values["PGHOST"], "compose-db.private")
                self.assertEqual(values["PGSSLMODE"], mode)
                self.assertNotIn("PGSSLROOTCERT", values)
                self.assertFalse(any(volume["name"] == "postgres-ca" for volume in pod["volumes"]))

    def test_ingress_drain_order_and_private_policy(self):
        pod = self.pod("ingress")
        sidecar = pod["initContainers"][0]
        policy = pod["containers"][0]
        self.assertEqual(sidecar["restartPolicy"], "Always")
        self.assertIn("/livez", " ".join(sidecar["startupProbe"]["exec"]["command"]))
        self.assertEqual(env(sidecar)["PROXY_ROUTER_PUBLIC_BIND_ADDRESS"], "$(POD_IP)")
        self.assertEqual(env(policy)["PROXY_PROTOCOL_BIND_ADDRESS"], "127.0.0.1")
        self.assertEqual(env(policy)["PROXY_PROTOCOL_TRUSTED_FROM"], "127.0.0.1/32")
        self.assertIn("sleep 5", " ".join(policy["lifecycle"]["preStop"]["exec"]["command"]))
        repository = ROOT.parents[1]
        self.assertIn("STOPSIGNAL SIGQUIT", (repository / "proxy/Dockerfile").read_text())
        self.assertIn("STOPSIGNAL SIGUSR1", (repository / "proxy-router/Dockerfile").read_text())

    def test_peer_rpc_h2_hop_is_published_and_allowed(self):
        """devshard phase 6: peers dial {InferenceUrl.host}:9443 (HTTP/2); the proxy
        forwards to the routers' 8081 listen; children inherit the dial settings."""
        ingress = self.pod("ingress")
        sidecar = ingress["initContainers"][0]
        self.assertIn(9443, [p["containerPort"] for p in sidecar["ports"]])
        self.assertEqual(env(sidecar)["DEVSHARD_RPC_H2_PORT"], "9443")
        self.assertEqual(env(sidecar)["DEVSHARD_RPC_H2_ROUTER_PORT"], "8081")
        public = self.objects[("Service", "test-gonka-ha-ingress")]["spec"]["ports"]
        self.assertIn(("rpc-h2", 9443), [(p["name"], p["port"]) for p in public])
        router = self.pod("router")["containers"][0]
        self.assertIn(8081, [p["containerPort"] for p in router["ports"]])
        router_service = self.objects[("Service", "test-gonka-ha-router")]["spec"]["ports"]
        self.assertIn(8081, [p["port"] for p in router_service])
        supervisor = env(self.pod("versiond")["containers"][0])
        self.assertEqual(supervisor["DEVSHARD_RPC_H2_UPGRADE"], "true")
        self.assertEqual(supervisor["DEVSHARD_RPC_H2_PORT"], "9443")
        allowed = {}
        for policy in (x for x in self.docs if x["kind"] == "NetworkPolicy"):
            component = policy["spec"]["podSelector"]["matchLabels"]["app.kubernetes.io/component"]
            allowed[component] = {rule["port"] for rule in policy["spec"]["ingress"][0]["ports"]}
        self.assertIn(9443, allowed["ingress"])
        self.assertIn(8081, allowed["router"])
        # The proxy-router entrypoint refuses an HTTPS InferenceUrl without
        # cert.pem and private.key, so the TLS Secret must reach it too.
        for secret, expected in (("edge-tls", True), (None, False)):
            with self.subTest(tlsSecret=secret):
                result = render({"ingress": {"tlsSecret": secret}})
                self.assertEqual(result.returncode, 0, result.stderr)
                docs = [x for x in yaml.load_all(result.stdout, Loader=UniqueKeyLoader) if x]
                pod = next(x for x in docs if x["kind"] == "StatefulSet"
                           and x["metadata"]["name"] == "test-gonka-ha-ingress")["spec"]["template"]["spec"]
                mounts = {m["mountPath"] for m in pod["initContainers"][0]["volumeMounts"]}
                self.assertEqual("/etc/haproxy/ssl" in mounts, expected)
                self.assertEqual(env(pod["initContainers"][0])["NGINX_MODE"], "both" if expected else "http")

    def test_no_control_plane_workloads_or_cluster_permissions(self):
        for item in self.docs:
            self.assertNotIn(item["kind"], ["ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding", "Secret"])
            if item["kind"] not in ("StatefulSet", "Deployment"):
                continue
            pod = item["spec"]["template"]["spec"]
            self.assertFalse(pod["automountServiceAccountToken"])
            self.assertFalse(pod.get("hostNetwork", False))
            self.assertTrue(pod["affinity"]["podAntiAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"])
            self.assertNotIn(item["metadata"]["name"], ["dapi", "node", "tmkms"])
        self.assertEqual(len([x for x in self.docs if x["kind"] == "NetworkPolicy"]), 5)

    def test_liveness_does_not_depend_on_shared_database(self):
        self.assertEqual(self.pod("versiond")["containers"][0]["livenessProbe"]["httpGet"]["path"], "/healthz")
        self.assertEqual(self.pod("versiond")["containers"][0]["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(self.pod("router")["containers"][0]["livenessProbe"]["httpGet"]["path"], "/livez")

    def test_reject_unsafe_configuration(self):
        invalid = [
            {"protocols": ["v3"]}, {"protocols": ["v6", "v6"]},
            {"protocols": ["v6/invalid"]},
            {"protocols": ["v6:invalid"]},
            {"postgres": {"host": ""}},
            {"postgres": {"user": ""}},
            {"postgres": {"passwordKey": ""}},
            {"postgres": {"sslMode": "verify-full", "caSecret": ""}},
            {"postgres": {"sslMode": "unknown"}},
            {"versiond": {"replicas": 1}},
            {"versiond": {"replicas": 2}},
            {"router": {"replicas": 2}},
            {"external": {"chainGrpcTls": "true"}},
            {"external": {"chainGrpcUrl": "", "chainGrpcTls": False}},
            {"versiond": {"drainAnnounceSeconds": 1}},
            {"versiond": {"terminationGracePeriodSeconds": 1500}},
            {"versiond": {"extraEnv": [{"name": "DEVSHARD_STORAGE_MODE", "value": "sqlite"}]}},
            {"ingress": {"policyExtraEnv": [{"name": "PROXY_PROTOCOL", "value": "false"}]}},
        ]
        for config in invalid:
            with self.subTest(config=config):
                self.assertNotEqual(render(config).returncode, 0)

    def test_custom_namespace_domain_and_replica_count(self):
        result = render({"clusterDomain": "cluster.example", "versiond": {"replicas": 4}})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("versiond-3.gonka.svc.cluster.example", result.stdout)

    def test_unused_dns_pool_capacity_is_not_an_operator_setting(self):
        result = render({"router": {"poolSlots": 64}})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("poolSlots", result.stderr)

    def test_native_sidecars_require_supported_kubernetes(self):
        self.assertNotEqual(render(kube="1.28.0").returncode, 0)


spec = importlib.util.spec_from_file_location("oracle", CHART / "files/oracle.py")
oracle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(oracle)


class OracleContract(unittest.TestCase):
    def test_filters_legacy_preserving_governance_artifact(self):
        artifact = {"name": "v6", "url": "https://example.com/bin", "sha256": "abc", "future": 7}
        result = oracle.project({"versions": [{"name": "v3"}, artifact], "epoch": 10}, {"v6"})
        self.assertEqual(result, {"versions": [artifact], "epoch": 10})

    def test_bad_catalog_is_error_not_empty_desired_set(self):
        for bad in [None, {}, {"versions": {}}, {"versions": [None]},
                    {"versions": [{"name": "v6"}, {"name": "v6"}]},
                    {"versions": [{"name": "../v6"}]}]:
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                oracle.project(bad, {"v6"})

    def test_unapproved_version_not_invented(self):
        self.assertEqual(oracle.project({"versions": [{"name": "v5"}]}, {"v6"}), {"versions": []})

    def test_shared_router_protocol_grammar(self):
        for name in ["v6+fix", "v6~candidate", "v6.1"]:
            self.assertEqual(oracle.project({"versions": [{"name": name}]}, {name})["versions"][0]["name"], name)
        with self.assertRaises(ValueError):
            oracle.project({"versions": [{"name": "v6:wrong"}]}, {"v6:wrong"})


if __name__ == "__main__":
    unittest.main()
