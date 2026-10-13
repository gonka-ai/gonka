#!/usr/bin/env python3
"""Exercise the real updater's rollback on an isolated Docker Compose project.

Only the failure trigger is injected. Container replacement, healthchecks,
configuration hashes, saved specifications and restoration use real Docker.
"""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import uuid

SOURCE = Path(__file__).resolve().parent
DOCKER = shutil.which("docker")


def command(*args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, check=True, **kwargs)


class RollbackTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        command(DOCKER, "info")
        cls.image = os.environ.get("BUSYBOX_IMAGE", "busybox:1.36")
        cls.image = json.loads(command(DOCKER, "image", "inspect", cls.image).stdout)[0]["Id"]

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="gonka-updater-rollback-")
        self.root = Path(self.temp.name)
        self.join = self.root / "join"
        self.join.mkdir()
        self.project = "gonka-rollback-" + uuid.uuid4().hex[:10]
        for name in ("update-devshard.sh", "deployment-lock.sh", "updater-rollback.sh", "updater-container-state.py"):
            shutil.copy2(SOURCE / name, self.join / name)
        for name in ("old", "new"):
            (self.root / name).mkdir()
            (self.root / name / "value").write_text(name)
        self.compose_file = self.join / "docker-compose.yml"
        (self.join / "config.env").touch()
        self.env = dict(os.environ, UPDATE_STATE_DIR=str(self.root / "state"),
                        UPDATE_WAIT_TIMEOUT_SECONDS="10", COMPOSE_FILE=str(self.compose_file),
                        GONKA_CONFIG_ENV=str(self.join / "config.env"))
        for key in ("GONKA_DEPLOYMENT_LOCK", "GONKA_DEPLOYMENT_KEY", "GONKA_DEPLOYMENT_LOCK_HELD", "COMPOSE_PROJECT_NAME"):
            self.env.pop(key, None)
        service = {"image": self.image, "command": ["sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done"],
                   "healthcheck": {"test": ["CMD", "true"], "interval": "1s", "timeout": "1s", "retries": 1},
                   "stop_grace_period": "1s", "environment": {"RELEASE": "old"},
                   "networks": {"default": {"aliases": ["original-peer"]}}}
        self.old = {"name": self.project, "services": {"versiond": copy.deepcopy(service), "proxy": copy.deepcopy(service)},
                    "networks": {"default": {}}}
        self.old["services"]["proxy"].update(volumes=[str(self.root / "old") + ":/config:ro", "/data"],
                                            ports=["127.0.0.1::8080"])
        self.compose_file.write_text(json.dumps(self.old))
        self.compose("up", "-d", "--wait", "--wait-timeout", "10")
        self.before = {name: self.inspect(name) for name in self.old["services"]}
        command(DOCKER, "exec", self.before["proxy"]["Id"], "sh", "-c", "echo preserved >/data/value")
        self.candidate = copy.deepcopy(self.old)
        for spec in self.candidate["services"].values():
            spec["environment"]["RELEASE"] = "candidate"
            spec["networks"]["default"]["aliases"] = ["candidate-peer"]
        self.candidate["services"]["proxy"]["volumes"][0] = str(self.root / "new") + ":/config:ro"
        for name in ("proxy-policy", "proxy-policy2"):
            self.candidate["services"][name] = copy.deepcopy(service)
        self.compose_file.write_text(json.dumps(self.candidate))
        self.wrapper = self.root / "docker-wrapper"
        self.wrapper.write_text('''#!/usr/bin/env bash
set -eu
if [[ $1 == compose && " $* " == *" pull "* ]]; then
    if [[ -n ${MOVE_IMAGE_TAG:-} ]]; then "$REAL_DOCKER" tag "$NEW_IMAGE" "$MOVE_IMAGE_TAG"; exit 0; fi
    exit 17
fi
if [[ $1 == compose && " $* " == *" up -d "* && ${!#} == "${FAIL_SERVICE:-}" && ! -e "$FAIL_ONCE" ]]; then
    "$REAL_DOCKER" "$@"
    touch "$FAIL_ONCE"
    if [[ ${KILL_UPDATER:-false} == true ]]; then kill -KILL "$PPID"; else exit 42; fi
else
    exec "$REAL_DOCKER" "$@"
fi
''')
        self.wrapper.chmod(0o755)
        self.env.update(DOCKER_BIN=str(self.wrapper), REAL_DOCKER=DOCKER, FAIL_ONCE=str(self.root / "injected"))

    def tearDown(self):
        command(DOCKER, "compose", "-p", self.project, "-f", str(self.compose_file), "down", "-v", "--timeout", "1")
        for record in self.before.values():
            for mount in record.get("Mounts", []):
                if mount["Type"] == "volume":
                    subprocess.run([DOCKER, "volume", "rm", mount["Name"]], capture_output=True)
        state = self.root / "state" / "previous"
        if state.exists():
            for path in state.glob("*.json"):
                # Tags include deployment identity; only remove this test's pins.
                images = command(DOCKER, "image", "ls", "--format", "{{.Repository}}", "gonka-previous/*").stdout.splitlines()
                for image in images:
                    if image.endswith("/" + path.stem):
                        labels = json.loads(path.read_text())
                        if labels["GonkaProject"] == self.project:
                            daemon = command(DOCKER, "info", "--format", "{{.ID}}").stdout.strip()
                            import hashlib
                            key = hashlib.sha256((daemon + "\n" + self.project + "\n").encode()).hexdigest()[:16]
                            if image == "gonka-previous/" + key + "/" + path.stem:
                                command(DOCKER, "image", "rm", image)
        self.temp.cleanup()

    def compose(self, *args):
        return command(DOCKER, "compose", "-f", str(self.compose_file), *args, env=self.env)

    def inspect(self, service):
        container = self.compose("ps", "--all", "--quiet", service).stdout.strip()
        return json.loads(command(DOCKER, "inspect", container).stdout)[0] if container else None

    def update(self, failure="", kill=False, *args):
        return subprocess.run([str(self.join / "update-devshard.sh"), *args], text=True, capture_output=True,
                              timeout=120, env=dict(self.env, FAIL_SERVICE=failure, KILL_UPDATER=str(kill).lower()))

    def assert_restored(self, service):
        current, old = self.inspect(service), self.before[service]
        self.assertEqual(current["Image"], old["Image"])
        self.maxDiff = None
        expected_config = dict(old["Config"], Image=old["Image"])
        self.assertEqual(current["Config"], expected_config)
        for key in ("Binds", "RestartPolicy", "NetworkMode", "ReadonlyRootfs", "Memory"):
            self.assertEqual(current["HostConfig"][key], old["HostConfig"][key], key)
        self.assertEqual(current["NetworkSettings"]["Ports"], old["NetworkSettings"]["Ports"])
        for network, endpoint in old["NetworkSettings"]["Networks"].items():
            self.assertEqual(current["NetworkSettings"]["Networks"][network]["Aliases"], endpoint["Aliases"])
        self.assertEqual(current["State"]["Health"]["Status"], "healthy")
        if service == "proxy":
            self.assertEqual(command(DOCKER, "exec", current["Id"], "cat", "/data/value").stdout.strip(), "preserved")
            self.assertEqual(command(DOCKER, "exec", current["Id"], "cat", "/config/value").stdout, "old")

    def test_initial_proxy_failure_restores_full_specification(self):
        result = self.update("proxy")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restoring the previous service specifications", result.stderr)
        self.assert_restored("proxy")
        self.assertEqual(self.inspect("versiond")["Id"], self.before["versiond"]["Id"])
        self.assertIsNone(self.inspect("proxy-policy"))
        self.assertIsNone(self.inspect("proxy-policy2"))
        self.assertFalse((self.root / "state/pending").exists())

    def test_policy_failure_restores_public_group(self):
        result = self.update("proxy-policy")
        self.assertNotEqual(result.returncode, 0)
        self.assert_restored("proxy")
        self.assertIsNone(self.inspect("proxy-policy"))
        self.assertIsNone(self.inspect("proxy-policy2"))

    def test_replica_failure_restores_configuration(self):
        result = self.update("versiond")
        self.assertNotEqual(result.returncode, 0)
        self.assert_restored("versiond")
        self.assertIn("RELEASE=candidate", self.inspect("proxy")["Config"]["Env"])

    def test_readiness_failure_restores_previous_healthcheck(self):
        self.candidate["services"]["versiond"]["healthcheck"]["test"] = ["CMD", "false"]
        self.compose_file.write_text(json.dumps(self.candidate))
        result = self.update()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not become healthy", result.stderr)
        self.assert_restored("versiond")

    def test_moving_image_tag_cannot_change_saved_image(self):
        tag = self.project + ":mutable"
        candidate = command(DOCKER, "commit", "--change", "LABEL candidate=true", self.before["versiond"]["Id"]).stdout.strip()
        try:
            command(DOCKER, "tag", self.image, tag)
            self.candidate["services"]["versiond"]["image"] = tag
            self.compose_file.write_text(json.dumps(self.candidate))
            self.env.update(MOVE_IMAGE_TAG=tag, NEW_IMAGE=candidate)
            result = self.update("versiond")
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(json.loads(command(DOCKER, "image", "inspect", tag).stdout)[0]["Id"], candidate)
            self.assert_restored("versiond")
        finally:
            subprocess.run([DOCKER, "image", "rm", tag, candidate], capture_output=True)

    def test_noop_preserves_previous_release(self):
        result = self.update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        directory = self.root / "state/previous"
        previous = {p.name: p.read_bytes() for p in directory.glob("*.json")}
        ids = {name: self.inspect(name)["Id"] for name in self.candidate["services"]}
        result = self.update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(previous, {p.name: p.read_bytes() for p in directory.glob("*.json")})
        self.assertEqual(ids, {name: self.inspect(name)["Id"] for name in self.candidate["services"]})
        self.assertEqual(json.loads(previous["proxy.json"])["Config"]["Env"], self.before["proxy"]["Config"]["Env"])

    def test_lock_shared_across_config_paths_and_commands(self):
        script = 'source "$1"; gonka_acquire_deployment_lock "$2" "$3"; printf "locked\\n"; read -r _'
        holder = subprocess.Popen(["bash", "-ec", script, "bash", str(SOURCE / "deployment-lock.sh"),
                                   str(self.root), self.project], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, text=True, env=self.env)
        try:
            self.assertEqual(holder.stdout.readline().strip(), "locked")
            other = self.root / "other-config"
            other.mkdir()
            (other / "config.env").touch()
            self.env.update(GONKA_CONFIG_ENV=str(other / "config.env"), XDG_RUNTIME_DIR=str(other), TMPDIR=str(other))
            for args in ((), ("--check",), ("--dry-run",)):
                result = self.update("", False, *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("another deployment operation holds", result.stderr)
            self.assertEqual(self.inspect("proxy")["Id"], self.before["proxy"]["Id"])
        finally:
            holder.communicate("release\n", timeout=10)

    def test_killed_update_recovers_before_retry(self):
        result = self.update("proxy", kill=True)
        self.assertEqual(result.returncode, -9, result.stdout + result.stderr)
        self.assertTrue((self.root / "state/pending").exists())
        check = self.update("", False, "--check")
        self.assertNotEqual(check.returncode, 0)
        self.assertIn("pending recovery", check.stderr)
        # Fail the same boundary again: retry must recover the old nginx first,
        # so the rollback anchor must still be the original specification.
        (self.root / "injected").unlink()
        retry = self.update("proxy")
        self.assertNotEqual(retry.returncode, 0)
        self.assertIn("Recovering the previous service specifications", retry.stdout)
        self.assert_restored("proxy")
        self.assertFalse((self.root / "state/pending").exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
