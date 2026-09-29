"""Safety decisions independent of Kubernetes timing or available clusters."""
import argparse
import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("rollout", Path(__file__).resolve().parents[1] / "rollout.py")
rollout = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rollout)


class ReserveTests(unittest.TestCase):
    def test_last_v7_owner_cannot_be_stopped_despite_three_v6_backends(self):
        ready = {"v6": {"pod-0", "pod-1", "pod-2"}, "v7": {"pod-2"}}
        with self.assertRaisesRegex(RuntimeError, "v7 has only 0"):
            rollout.require_reserve({"v6", "v7"}, ready, {}, "pod-2", 2)

    def test_unadmitted_reserve_does_not_count(self):
        ready = {"v6": {"pod-0", "pod-1", "pod-2"}}
        parents = {"ingress-0": {"v6": {"pod-0", "pod-2"}}}
        with self.assertRaisesRegex(RuntimeError, "ingress-0 admits only 1"):
            rollout.require_reserve({"v6"}, ready, parents, "pod-2", 2)

    def test_disappearing_protected_version_blocks_further_replacements(self):
        with self.assertRaisesRegex(RuntimeError, "v7 has only 0"):
            rollout.require_reserve({"v6", "v7"}, {"v6": {"a", "b"}, "v7": set()}, {}, "c", 2)

    def test_reserve_is_intersection_of_healthy_and_admitted(self):
        with self.assertRaisesRegex(RuntimeError, "admits only 1"):
            rollout.require_reserve({"v6"}, {"v6": {"a", "b", "c"}},
                                    {"parent": {"v6": {"a", "c", "stale"}}}, "c", 2)
        rollout.require_reserve({"v6"}, {"v6": {"a", "b", "c"}},
                                {"parent": {"v6": {"a", "b", "c"}}}, "c", 2)

    def test_stats_use_stable_service_address_and_only_up_servers(self):
        stats = "# pxname,svname,status,addr,\nbackend,a,UP,,\nbackend,b,DRAIN,,\nbackend,c,DOWN,,\nbackend,BACKEND,UP,,\nother,d,UP,,\n"
        addresses = rollout.server_addresses("1\n# be_id be_name srv_id srv_name srv_addr\n1 backend 1 a 10.0.1.2\n1 backend 2 b 10.0.1.3\n1 backend 3 c 10.0.1.4\n2 other 1 d 10.0.1.5\n")
        self.assertEqual(rollout.admitted_addresses(stats, "backend", addresses), {"10.0.1.2"})
        for bad in ("", "Unknown command", "# pxname,svname,status\n"):
            with self.assertRaises(RuntimeError):
                rollout.admitted_addresses(bad, "backend", addresses)
        with self.assertRaises(RuntimeError):
            rollout.admitted_addresses(stats, "backend", {})
        with self.assertRaises(RuntimeError):
            rollout.server_addresses("Unknown command")

    def test_map_errors_never_become_an_empty_catalog(self):
        self.assertEqual(rollout.route_map("0x123 v6 backend\n"), {"v6": "backend"})
        with self.assertRaises(RuntimeError):
            rollout.route_map("Can't find map")


class ReplacementTests(unittest.TestCase):
    def make_fleet(self):
        args = argparse.Namespace(namespace="ns", release="host", context=None, kubeconfig=None,
                                  timeout=1, resume=False)
        fleet = rollout.Fleet(args)
        fleet.versions = {"v6", "v7"}
        fleet.sets = {"versiond": {"metadata": {"name": "versiond", "generation": 1},
                                  "spec": {"template": {"spec": {"candidate": True}}},
                                  "status": {"updateRevision": "new"}}}
        pod = {"metadata": {"name": "pod-2", "uid": "old-uid",
                            "labels": {"controller-revision-hash": "old"}}}
        fleet.refresh = lambda: None
        fleet.pods = lambda _: [copy.deepcopy(pod)]
        fleet.discover = lambda: None
        fleet.get = lambda kind, *args: (fleet.sets["versiond"] if kind == "statefulset" else
                                        {"data": {"spec": {"template": {"spec": {"old": True}}}}})
        fleet.save = lambda: None
        fleet.wait = lambda *args: None
        return fleet

    def test_refusal_does_not_delete_or_start_a_replacement(self):
        fleet = self.make_fleet()
        events = []
        def refuse(*args):
            raise RuntimeError("v7 lacks reserve")
        fleet.guard = refuse
        fleet.delete = lambda p: events.append("delete")
        with self.assertRaisesRegex(RuntimeError, "lacks reserve"):
            fleet.replace_component("versiond")
        self.assertEqual(events, [])
        self.assertNotIn("replacement", fleet.state)

    def test_journal_precedes_delete_and_commit_waits_for_per_version_admission(self):
        fleet = self.make_fleet()
        events = []
        fleet.guard = lambda *args: events.append("reserve")
        fleet.save = lambda: events.append("journal" if "replacement" in fleet.state else "commit")
        fleet.delete = lambda p: events.append("delete")
        fleet.recovered = lambda *args: False
        def fail_wait(description, predicate):
            self.assertFalse(predicate())
            events.append("admission-failed")
            raise RuntimeError("candidate v7 missing")
        fleet.wait = fail_wait
        with self.assertRaisesRegex(RuntimeError, "candidate v7 missing"):
            fleet.replace_component("versiond")
        self.assertEqual(events, ["reserve", "journal", "delete", "admission-failed"])
        self.assertEqual(fleet.state["replacement"]["template"], {"spec": {"old": True}})

    def test_interruption_before_delete_keeps_original_generation(self):
        fleet = self.make_fleet()
        fleet.state["replacement"] = {"component": "versiond", "pod": "pod-2",
                                      "uid": "old-uid", "template": {"spec": {}}, "generation": 1,
                                      "target_template": fleet.sets["versiond"]["spec"]["template"]}
        events = []
        fleet.kub = lambda *args: events.append("patch")
        fleet.delete = lambda p: self.fail("original pod must not be deleted on resume")
        fleet.restore()
        self.assertEqual(events, ["patch"])
        self.assertNotIn("replacement", fleet.state)

    def test_concurrent_upgrade_after_journal_is_not_rolled_back(self):
        fleet = self.make_fleet()
        events = []
        fleet.guard = lambda *args: None
        def save():
            if "replacement" in fleet.state:
                fleet.sets = copy.deepcopy(fleet.sets)
                fleet.sets["versiond"]["metadata"]["generation"] = 2
        fleet.save = save
        fleet.delete = lambda p: events.append("delete")
        with self.assertRaisesRegex(RuntimeError, "changed concurrently"):
            fleet.replace_component("versiond")
        self.assertNotIn("replacement", fleet.state)
        self.assertEqual(events, [])

    def test_restore_does_not_overwrite_a_concurrent_template(self):
        fleet = self.make_fleet()
        fleet.state["replacement"] = {"component": "versiond", "pod": "pod-2",
                                      "uid": "old-uid", "template": {"spec": {}}, "generation": 0,
                                      "target_template": {"spec": {"unexpected": True}}}
        fleet.kub = lambda *args: self.fail("must not patch concurrent template")
        with self.assertRaisesRegex(RuntimeError, "changed concurrently"):
            fleet.restore()

    def test_resume_does_not_delete_an_already_restored_generation(self):
        fleet = self.make_fleet()
        template = fleet.sets["versiond"]["spec"]["template"]
        fleet.state["replacement"] = {"component": "versiond", "pod": "pod-2",
                                      "uid": "old-uid", "template": template,
                                      "previous_revision": "old"}
        restored = {"metadata": {"name": "pod-2", "uid": "restored-uid",
                                 "labels": {"controller-revision-hash": "old"}}}
        fleet.pods = lambda _: [restored]
        fleet.kub = lambda *args: self.fail("restored template needs no patch")
        fleet.delete = lambda p: self.fail("restored generation must not be drained again")
        checks = []
        fleet.recovered = lambda *args: checks.append(args) or True
        fleet.observed = lambda: True
        fleet.wait = lambda description, predicate: self.assertTrue(predicate())
        fleet.restore()
        self.assertEqual(checks, [("versiond", "pod-2", "old-uid", "old")])
        self.assertNotIn("replacement", fleet.state)

    def test_ready_foreign_revision_is_not_committed(self):
        fleet = self.make_fleet()
        pod = {"metadata": {"name": "pod-2", "uid": "foreign-uid",
                            "labels": {"controller-revision-hash": "foreign"}}}
        fleet.view = lambda _: ([pod], {"v6": {"pod-2"}, "v7": {"pod-2"}}, {})
        with self.assertRaisesRegex(RuntimeError, "unexpected replacement"):
            fleet.recovered("versiond", "pod-2", "old-uid", "new")

    def test_candidate_timeout_identifies_missing_version(self):
        fleet = self.make_fleet()
        pod = {"metadata": {"name": "pod-2", "uid": "new-uid",
                            "labels": {"controller-revision-hash": "new"}}}
        fleet.view = lambda _: ([pod], {"v6": {"pod-2"}, "v7": set()}, {})
        with self.assertRaisesRegex(RuntimeError, "missing for v7"):
            fleet.recovered("versiond", "pod-2", "old-uid", "new")

    def test_protected_protocol_removal_is_rejected(self):
        fleet = self.make_fleet()
        with self.assertRaisesRegex(RuntimeError, "retire protected protocols: v7"):
            fleet.require_protocols({"v6"})


if __name__ == "__main__":
    unittest.main()
