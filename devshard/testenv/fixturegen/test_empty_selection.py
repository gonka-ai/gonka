import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parent


class EmptySelectionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        for name in ("run.sh", "check.py"):
            shutil.copy(SOURCE / name, self.root / name)

    def tearDown(self):
        self.tmp.cleanup()

    def check(self, root):
        return subprocess.run(
            ["python3", str(self.root / "check.py"), str(root)],
            capture_output=True, text=True,
        )

    def run_generator(self, models, *args):
        env = dict(os.environ, HF_CACHE=str(self.root), MODELS=str(models), OUT="fixtures")
        return subprocess.run(
            ["bash", str(self.root / "run.sh"), *args],
            cwd=self.root, env=env, capture_output=True, text=True,
        )

    def test_missing_and_empty_fixture_directories_fail(self):
        self.assertNotEqual(self.check(self.root / "missing").returncode, 0)
        model = self.root / "fixtures" / "model"
        model.mkdir(parents=True)
        (model / "summary.json").write_text("{}")
        self.assertNotEqual(self.check(self.root / "fixtures").returncode, 0)

    def test_rejected_fixture_is_a_result(self):
        model = self.root / "fixtures" / "model"
        model.mkdir(parents=True)
        (model / "case.rejected.json").write_text(json.dumps({
            "case": "case", "rejected_by": "gateway", "detail": "invalid request",
        }))
        self.assertEqual(self.check(self.root / "fixtures").returncode, 0)

    def test_missing_models_file_fails(self):
        self.assertNotEqual(self.run_generator(self.root / "missing.tsv").returncode, 0)

    def test_unmatched_filter_fails_even_with_existing_fixture(self):
        models = self.root / "models.tsv"
        models.write_text("model\trevision\timage\nexample/model\trev\timage\n")
        model = self.root / "fixtures" / "model"
        model.mkdir(parents=True)
        (model / "case.rejected.json").write_text(json.dumps({
            "case": "case", "rejected_by": "gateway", "detail": "invalid request",
        }))
        self.assertNotEqual(self.run_generator(models, "unmatched").returncode, 0)


if __name__ == "__main__":
    unittest.main()
