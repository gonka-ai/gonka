"""Exercise Helm's live-upgrade guard with recorded Kubernetes object shapes.

The fixture calls the production helper with explicit lookup results. No test
switch or simulated cluster state is exposed through production chart values.
Real API-backed upgrade rejection is additionally covered by kind-smoke.sh.
"""
import copy
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

from test_charts import CHART, HELM, ROOT, UniqueKeyLoader, render


def objects(result):
    if result.returncode:
        raise AssertionError(result.stderr)
    return [item for item in yaml.load_all(result.stdout, Loader=UniqueKeyLoader) if item]


def lookup_state(documents, live=True):
    state = {
        "statefulSets": {"items": [copy.deepcopy(item) for item in documents if item["kind"] == "StatefulSet"]},
        "deployments": {"items": [copy.deepcopy(item) for item in documents if item["kind"] == "Deployment"]},
        "pods": {"items": []},
    }
    if live:
        for sts in state["statefulSets"]["items"]:
            labels = sts["spec"]["template"]["metadata"]["labels"]
            if labels["app.kubernetes.io/component"] in ("router", "ingress"):
                state["pods"]["items"].append({"metadata": {
                    "name": sts["metadata"]["name"] + "-0", "labels": copy.deepcopy(labels),
                }})
    else:
        for sts in state["statefulSets"]["items"]:
            if sts["spec"]["template"]["metadata"]["labels"]["app.kubernetes.io/component"] in ("router", "ingress"):
                sts["spec"]["replicas"] = 0
    return state


def guarded_render(state, overrides=None):
    with tempfile.TemporaryDirectory() as scratch:
        chart = Path(scratch) / "chart"
        shutil.copytree(CHART, chart)
        arguments = " ".join(
            f'{json.dumps(key)} (fromJson {json.dumps(json.dumps(value))})'
            for key, value in state.items()
        )
        (chart / "templates/guard-fixture.yaml").write_text(
            '{{- include "gonka.validateLiveUpgrade" (dict "root" . ' + arguments + ') -}}\n'
        )
        values = Path(scratch) / "values.yaml"
        values.write_text(yaml.safe_dump(overrides or {}))
        return subprocess.run(
            [HELM, "template", "test", str(chart), "--namespace", "gonka", "--is-upgrade",
             "--kube-version", "1.33.0", "-f", str(ROOT / "examples/values.yaml"), "-f", str(values)],
            text=True, capture_output=True,
        )


class UpgradeGuards(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents = objects(render())

    def test_serving_workloads_require_explicit_pod_replacement(self):
        for item in self.documents:
            if item["kind"] == "StatefulSet":
                self.assertEqual(item["spec"]["updateStrategy"], {"type": "OnDelete"})
                self.assertEqual(item["metadata"]["annotations"]["gonka.ai/desired-replicas"],
                                 str(item["spec"]["replicas"]))

    def test_ingress_preserves_the_configured_reserve_during_replacement(self):
        insufficient = render({"ingress": {"replicas": 2}})
        self.assertNotEqual(insufficient.returncode, 0)
        self.assertIn("ingress.replicas must preserve router.activationMinReady", insufficient.stderr)
        supported = render({"ingress": {"replicas": 2}, "router": {"activationMinReady": 1}})
        self.assertEqual(supported.returncode, 0, supported.stderr)

    def test_maintenance_keeps_router_tiers_off_and_records_desired_counts(self):
        for item in objects(render({"maintenance": True, "versiond": {"replicas": 4}})):
            if item["kind"] != "StatefulSet":
                continue
            component = item["spec"]["template"]["metadata"]["labels"]["app.kubernetes.io/component"]
            self.assertEqual(item["spec"]["replicas"], 4 if component == "versiond" else 0)
            self.assertEqual(item["metadata"]["annotations"]["gonka.ai/desired-replicas"],
                             "4" if component == "versiond" else "3")

    def test_live_image_upgrade_keeps_membership(self):
        result = guarded_render(lookup_state(self.documents), {"images": {"versiond": "example.invalid/versiond:next"}})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_live_replica_changes_names_and_endpoint_domain_are_rejected(self):
        cases = [({component: {"replicas": count}}, ("replica count", "versiond endpoints"))
                 for component in ("versiond", "router", "ingress") for count in (4, 2)]
        cases += [({"fullnameOverride": "renamed"}, ("StatefulSet names",)),
                  ({"clusterDomain": "other.internal"}, ("versiond endpoints",))]
        for overrides, reason in cases:
            # Preserve the ordinary rollout reserve while testing scale-down.
            if any(value.get("replicas") == 2 for value in overrides.values() if isinstance(value, dict)):
                overrides.setdefault("router", {})["activationMinReady"] = 1
            with self.subTest(overrides=overrides):
                result = guarded_render(lookup_state(self.documents), overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue(any(part in result.stderr for part in reason), result.stderr)

    def test_maintenance_is_not_a_live_bypass_even_for_terminating_pods(self):
        state = lookup_state(self.documents)
        for pod in state["pods"]["items"]:
            pod["metadata"]["deletionTimestamp"] = "2026-09-29T00:00:00Z"
        result = guarded_render(state, {"maintenance": True})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("finish draining first", result.stderr)

    def test_protocol_removal_is_rejected_but_addition_is_allowed(self):
        old = objects(render({"protocols": ["v5", "v6"]}))
        removed = guarded_render(lookup_state(old))
        self.assertNotEqual(removed.returncode, 0)
        self.assertIn("removing protocol v5", removed.stderr)
        added = guarded_render(lookup_state(self.documents), {"protocols": ["v6", "v7"]})
        self.assertEqual(added.returncode, 0, added.stderr)

    def test_pre_annotation_chart_uses_actual_membership(self):
        state = lookup_state(self.documents)
        for sts in state["statefulSets"]["items"]:
            sts["metadata"].pop("annotations", None)
        unchanged = guarded_render(state)
        self.assertEqual(unchanged.returncode, 0, unchanged.stderr)
        changed = guarded_render(state, {"versiond": {"replicas": 4}})
        self.assertNotEqual(changed.returncode, 0)
        self.assertTrue(any(part in changed.stderr for part in ("versiond replica count", "versiond endpoints")), changed.stderr)

    def test_offline_change_and_return_from_maintenance(self):
        result = guarded_render(lookup_state(self.documents, live=False),
                                {"maintenance": True, "versiond": {"replicas": 4}})
        updated = objects(result)
        state = lookup_state(updated)
        state["pods"]["items"] = [pod for pod in state["pods"]["items"]
                                  if pod["metadata"]["labels"]["app.kubernetes.io/component"] == "router"]
        # The operator starts routers privately, verifies them, then reopens ingress.
        for sts in state["statefulSets"]["items"]:
            if sts["metadata"]["name"].endswith("-router"):
                sts["spec"]["replicas"] = 3
        restored = guarded_render(state, {"versiond": {"replicas": 4}})
        self.assertEqual(restored.returncode, 0, restored.stderr)

    def test_offline_membership_change_still_requires_explicit_maintenance(self):
        result = guarded_render(lookup_state(self.documents, live=False), {"versiond": {"replicas": 4}})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("maintenance=true", result.stderr)

    def test_rename_is_rejected_even_after_all_routing_pods_have_gone(self):
        result = guarded_render(lookup_state(self.documents, live=False),
                                {"maintenance": True, "fullnameOverride": "renamed"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("StatefulSet names is unsupported", result.stderr)

    def test_explicit_override_preserving_existing_names_is_allowed(self):
        result = guarded_render(lookup_state(self.documents), {"fullnameOverride": "test-gonka-ha"})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_fresh_install_has_no_previous_membership(self):
        result = guarded_render({name: {"items": []} for name in ("pods", "statefulSets", "deployments")},
                                {"fullnameOverride": "custom-name"})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_other_releases_do_not_block_maintenance(self):
        state = lookup_state(self.documents)
        for pod in state["pods"]["items"]:
            pod["metadata"]["labels"]["app.kubernetes.io/instance"] = "someone-else"
        for sts in state["statefulSets"]["items"]:
            sts["metadata"]["labels"]["app.kubernetes.io/instance"] = "someone-else"
        result = guarded_render(state, {"maintenance": True, "versiond": {"replicas": 4}})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_empty_routing_controllers_must_be_scaled_to_zero_first(self):
        state = lookup_state(self.documents)
        state["pods"]["items"] = []
        for overrides in ({"maintenance": True}, {"versiond": {"replicas": 4}}):
            with self.subTest(overrides=overrides):
                result = guarded_render(state, overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("maintenance", result.stderr)

    def test_exposing_existing_ingress_does_not_change_membership(self):
        result = guarded_render(lookup_state(self.documents), {"ingress": {"service": {"type": "LoadBalancer"}}})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_controller_fails_closed_while_routing_pods_exist(self):
        for kind in ("statefulSets", "deployments"):
            with self.subTest(kind=kind):
                state = lookup_state(self.documents)
                state[kind]["items"] = []
                result = guarded_render(state)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("cannot verify live", result.stderr)


if __name__ == "__main__":
    unittest.main()
