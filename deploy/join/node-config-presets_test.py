#!/usr/bin/env python3
"""Check the MLNode presets in deploy/join/node-config-*.json.

An operator switches a running node between a preset and its `-prefill-max` /
`-decode-max` variants with `PUT /admin/v1/nodes/<id>` (docs/mlnode-configs.md).
A variant that serves another checkpoint than its base preset changes the
deployment fingerprint, so DAPI reloads vLLM with a different model instead of
only new engine arguments. The override checks mirror
`decentralized-api/apiconfig/config.go` (validateNodeConfig).
"""
from pathlib import Path
import json
import re
import unittest

SOURCE = Path(__file__).resolve().parent
PRESETS = sorted(SOURCE.glob("node-config-*.json"))
RESERVED_OVERRIDE_ARGS = {"--model", "--revision", "--served-model-name"}
VARIANT = re.compile(r"(?P<base>node-config-.+)-(?P<kind>prefill|decode)-max\.json")


def load(path):
    with path.open(encoding="utf-8") as f:
        return json.load(f)


def models(path):
    return {model_id: config for node in load(path) for model_id, config in node["models"].items()}


def flag(args, name):
    for i, arg in enumerate(args):
        if arg == name and i + 1 < len(args):
            return args[i + 1]
        if arg.startswith(name + "="):
            return arg.split("=", 1)[1]
    return None


class PresetTest(unittest.TestCase):
    def test_presets_found(self):
        self.assertTrue(PRESETS, "no node-config-*.json next to this test")

    def test_model_overrides_pass_dapi_validation(self):
        for path in PRESETS:
            for model_id, config in models(path).items():
                override = config.get("model_override")
                if override is None:
                    continue
                with self.subTest(preset=path.name, model=model_id):
                    self.assertTrue(override.get("hf_repo", "").strip(), "hf_repo is required")
                    self.assertRegex(override.get("hf_commit", ""), r"^[0-9a-f]{40}$")
                    keys = {arg.split("=", 1)[0] for arg in config.get("args", [])}
                    self.assertFalse(keys & RESERVED_OVERRIDE_ARGS,
                                     "an override sets --model, --revision and --served-model-name itself")

    def test_nvfp4_presets_load_an_nvfp4_checkpoint(self):
        for path in PRESETS:
            if "-nvfp4" not in path.name:
                continue
            for model_id, config in models(path).items():
                with self.subTest(preset=path.name, model=model_id):
                    override = config.get("model_override")
                    self.assertIsNotNone(override, "an -nvfp4 preset without model_override loads " + model_id)
                    self.assertIn("nvfp4", override["hf_repo"].lower())

    def test_max_variants_serve_the_base_checkpoint(self):
        for path in PRESETS:
            match = VARIANT.fullmatch(path.name)
            if not match:
                continue
            base = SOURCE / (match["base"] + ".json")
            if not base.exists():
                continue
            with self.subTest(preset=path.name, base=base.name):
                variant_models, base_models = models(path), models(base)
                self.assertEqual(sorted(variant_models), sorted(base_models))
                for model_id in base_models:
                    self.assertEqual(variant_models[model_id].get("model_override"),
                                     base_models[model_id].get("model_override"))

    def test_decode_max_presets_do_not_use_pipeline_parallelism(self):
        # docs/mlnode-configs.md: DECODE PoC stalls the engine under pipeline parallelism.
        for path in PRESETS:
            if not path.name.endswith("-decode-max.json"):
                continue
            for model_id, config in models(path).items():
                with self.subTest(preset=path.name, model=model_id):
                    self.assertIn(flag(config.get("args", []), "--pipeline-parallel-size"), (None, "1"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
