import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("verifier", Path(__file__).resolve().parents[1] / "verify-storage.py")
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class StorageVerifier(unittest.TestCase):
    def exercise(self, *, split=False, reference_split=False, snapshot_change=False):
        values = {}
        reads = {}

        def proof(pod):
            reads[pod] = reads.get(pod, 0) + 1
            return {"identity": "same-cloned-id", "snapshot": f"snapshot-{pod}" +
                    ("-changed" if snapshot_change and reads[pod] > 1 else ""),
                    "children": 1, "targets": [{"version": "v6", "generation": f"gen-{pod}"}]}

        def challenge(pod, request):
            database = pod if split else "shared"
            if request["operation"] == "write":
                values[database] = request["nonce"]
            return {"identity": "same-cloned-id", "snapshot": request["snapshot"],
                    "generation": request["generation"], "found": values.get(database) == request["nonce"]}

        return verifier.verify(["a", "b"], "same-cloned-id", proof, challenge,
                               lambda: "same-cloned-id|" + ("wrong" if reference_split else values.get("shared", "")))

    def test_all_children_share_independent_primary(self):
        self.assertEqual(self.exercise(), 2)

    def test_identical_database_identity_does_not_allow_cloned_writers(self):
        with self.assertRaisesRegex(RuntimeError, "challenge failed"):
            self.exercise(split=True)

    def test_wrong_reference_database_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "reference primary"):
            self.exercise(reference_split=True)

    def test_generation_swap_during_check_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "generations changed"):
            self.exercise(snapshot_change=True)
