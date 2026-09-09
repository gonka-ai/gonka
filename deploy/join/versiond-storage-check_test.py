#!/usr/bin/env python3
"""Exercise --check-storage with Docker, BusyBox, psql and real databases.

Only the versiond proof API is a stand-in; its addressed writes go to actual
PostgreSQL databases. No running deployment or fixed container names are used.
"""

import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest
import uuid

SOURCE = Path(__file__).resolve().parent


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs)


class StorageCheck(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="gonka-storage-check-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.root = Path(cls.tmp.name)
        cls.prefix = "gonka-storage-check-" + uuid.uuid4().hex[:12]
        cls.pg = cls.prefix + "-pg"
        cls.members = [cls.prefix + "-versiond", cls.prefix + "-versiond2"]
        cls.image = cls.prefix + ":test"
        cls.identity = str(uuid.uuid4())
        cls.join = cls.root / "join"
        cls.join.mkdir()
        # Deliberately no config.env, Compose files, node, api, proxy or fleet.
        for name in ("update-devshard.sh", "versiond-storage-check.sh", "deployment-lock.sh"):
            shutil.copy2(SOURCE / name, cls.join / name)
        build = cls.root / "build"
        build.mkdir()
        shutil.copy2(SOURCE / "testdata/storage-check-server.py", build / "server.py")
        (build / "Dockerfile").write_text(
            "FROM python:3.12-alpine\nRUN apk add --no-cache postgresql-client\n"
            "COPY server.py /server.py\nCMD [\"python3\", \"/server.py\"]\n"
        )
        cls.addClassCleanup(lambda: subprocess.run(
            ["docker", "image", "rm", cls.image], capture_output=True))
        run("docker", "build", "-q", "-t", cls.image, str(build))
        run("docker", "network", "create", cls.prefix)
        cls.addClassCleanup(lambda: subprocess.run(
            ["docker", "network", "rm", cls.prefix], capture_output=True))
        # Registered before creation so partial setup is cleaned up too.
        cls.addClassCleanup(lambda: subprocess.run(
            ["docker", "rm", "-fv", *cls.members, cls.pg], capture_output=True))
        run("docker", "run", "-d", "--name", cls.pg, "--network", cls.prefix,
            "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=test-password",
            "-e", "POSTGRES_DB=reference", "postgres:16-alpine")
        port = json.loads(run("docker", "inspect", cls.pg).stdout)[0][
            "NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
        cls.pg_env = dict(os.environ, PGHOST="127.0.0.1", PGPORT=port,
                          PGUSER="postgres", PGPASSWORD="test-password",
                          PGDATABASE="reference", PGCONNECT_TIMEOUT="2")
        for _ in range(60):
            try:
                cls.sql("SELECT 1")
                break
            except subprocess.CalledProcessError:
                time.sleep(0.5)
        else:
            raise RuntimeError("test PostgreSQL failed to start")
        cls.sql("CREATE DATABASE independent_clone")
        cls.sql("CREATE DATABASE different")
        for db in ("reference", "independent_clone", "different"):
            identity = str(uuid.uuid4()) if db == "different" else cls.identity
            cls.sql("CREATE TABLE devshard_storage_identity ("
                    "singleton boolean PRIMARY KEY CHECK (singleton), "
                    "identity uuid NOT NULL, challenge uuid); "
                    f"INSERT INTO devshard_storage_identity VALUES (true, '{identity}', NULL)", db)
        cls.reference = cls.root / "pool-postgres.env"
        cls.reference.write_text("\n".join(
            f"{key}='{cls.pg_env[key]}'"
            for key in ("PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE")) + "\n")
        cls.reference.chmod(0o600)
        cls.fixture_dirs = []
        for i, member in enumerate(cls.members):
            fixture = cls.root / f"fixture-{i}"
            fixture.mkdir()
            cls.fixture_dirs.append(fixture)
            cls.configure(i)
            run("docker", "run", "-d", "--name", member, "--network", cls.prefix,
                "-e", f"PGHOST={cls.pg}", "-e", "PGUSER=postgres",
                "-e", "PGPASSWORD=test-password", "-e", "PGDATABASE=reference",
                "-v", f"{fixture}:/fixture:ro,z", cls.image)
            for _ in range(40):
                ready = subprocess.run(
                    ["docker", "exec", member, "/bin/busybox", "wget", "-qO-",
                     "http://127.0.0.1:8080/readyz"], capture_output=True)
                if ready.returncode == 0:
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError("test proof API failed to start")

    @classmethod
    def sql(cls, statement, database="reference"):
        return run("psql", "-XwqAt", "-v", "ON_ERROR_STOP=1", "-c", statement,
                   env=dict(cls.pg_env, PGDATABASE=database)).stdout.strip()

    @classmethod
    def configure(cls, member=0, *, mode="normal", databases=None):
        settings = dict(token=uuid.uuid4().hex, mode=mode,
                        databases=databases or ["reference", "reference"])
        (cls.fixture_dirs[member] / "settings.json").write_text(json.dumps(settings))

    def setUp(self):
        for i in range(len(self.members)):
            self.configure(i)
        self.sql("UPDATE devshard_storage_identity SET challenge = NULL")

    def check(self, *, members=None, reference=None, extra=(), lock_path=None):
        args = [str(self.join / "update-devshard.sh"), "--check-storage",
                "--reference-env", str(reference or self.reference)]
        for member in members or self.members[:1]:
            args.extend(["--container", member])
        # These inherited settings must never override the reference file.
        env = dict(os.environ, PGHOST="wrong.invalid", PGDATABASE="wrong",
                   PGPASSWORD="inherited-secret", PGSERVICE="wrong-service",
                   PGOPTIONS="-c search_path=wrong", GONKA_CONFIG_ENV=str(self.join / "config.env"))
        if lock_path:
            env["GONKA_DEPLOYMENT_LOCK"] = str(lock_path)
        result = subprocess.run([*args, *extra], capture_output=True, text=True,
                                env=env, timeout=90)
        self.assertNotIn("test-password", result.stdout + result.stderr)
        self.assertNotIn("inherited-secret", result.stdout + result.stderr)
        return result

    def assert_failed(self, result, message):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("Storage check passed", result.stdout)
        self.assertIn(message, result.stderr)

    def test_shared_database_and_multiple_containers(self):
        before = run("docker", "inspect", "--format", "{{.State.StartedAt}}", *self.members).stdout
        result = self.check(members=self.members)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("4 HA process(es) in 2 container(s)", result.stdout)
        self.assertEqual(before, run("docker", "inspect", "--format",
                                     "{{.State.StartedAt}}", *self.members).stdout)

    def test_clone_with_equal_identity_in_second_generation(self):
        self.configure(databases=["reference", "independent_clone"])
        self.assert_failed(self.check(), "generation 1): reference PostgreSQL did not receive")

    def test_different_database(self):
        self.configure(databases=["different"])
        self.assert_failed(self.check(), "identity differs")

    def test_all_proofs_required(self):
        for mode in ("identity-only", "empty", "duplicate", "incomplete"):
            with self.subTest(mode=mode):
                self.configure(mode=mode)
                self.assert_failed(self.check(), "invalid storage proof")

    def test_unavailable_and_legacy_proofs(self):
        for mode in ("unavailable", "legacy"):
            with self.subTest(mode=mode):
                self.configure(mode=mode)
                self.assert_failed(self.check(), "storage proof unavailable")

    def test_write_requires_writable_application_connection(self):
        self.configure(mode="readonly")
        self.assert_failed(self.check(), "cannot write storage challenge")

    def test_process_replacement_during_check(self):
        self.configure(mode="stale")
        self.assert_failed(self.check(), "processes changed during verification")

    def test_invalid_challenge_response(self):
        self.configure(mode="bad-response")
        self.assert_failed(self.check(), "invalid challenge response")

    def test_unready_replica(self):
        self.configure(mode="unready")
        self.assert_failed(self.check(), "versiond is not ready")

    def test_missing_replica(self):
        self.assert_failed(self.check(members=[self.prefix + "-missing"]), "cannot inspect container")

    def test_bad_reference_connection(self):
        bad = self.root / "bad-reference.env"
        bad.write_text(self.reference.read_text() + "PGPASSWORD='wrong-password'\n")
        self.assert_failed(self.check(reference=bad), "cannot read the reference PostgreSQL")

    def test_updater_capacity_against_real_postgres(self):
        # Run the host updater's actual SQL preflight against this isolated
        # server. No Compose services are created by --check.
        directory = self.root / "capacity-join"
        directory.mkdir()
        for name in ("update-devshard.sh", "deployment-lock.sh", "updater-rollback.sh", "updater-container-state.py"):
            shutil.copy2(SOURCE / name, directory / name)
        (directory / "config.env").write_text("VERSIOND_VERSIONS='v4 v5 v6'\n")
        service = dict(image=self.image, networks=["default", "versiond-router-back"], environment=dict(GONKA_HA="true", DEVSHARD_STORAGE_MODE="postgres",
                       PGHOST=self.pg, PGDATABASE="reference", PGUSER="postgres", PGPASSWORD="test-password"))
        model = dict(name=self.prefix, services=dict(versiond=service, versiond2=service),
                     networks={"default": {"name": self.prefix}, "versiond-router-back": {"name": self.prefix}})
        for name in ("proxy", "proxy-policy", "proxy-policy2"):
            model["services"][name] = dict(image=self.image)
        compose = directory / "docker-compose.yml"
        compose.write_text(json.dumps(model))
        env = dict(os.environ, GONKA_CONFIG_ENV=str(directory / "config.env"), COMPOSE_FILE=str(compose),
                   UPDATE_STATE_DIR=str(directory / "state"))

        endpoints = directory / "endpoints.json"

        def capacity(value):
            self.sql(f"ALTER SYSTEM SET max_connections = {value}")
            run("docker", "restart", self.pg)
            self.pg_env["PGPORT"] = json.loads(run("docker", "inspect", self.pg).stdout)[0][
                "NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
            self.reference.write_text("\n".join(
                f"{key}='{self.pg_env[key]}'" for key in ("PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE")) + "\n")
            for _ in range(60):
                try:
                    self.sql("SELECT 1")
                    return
                except subprocess.CalledProcessError:
                    time.sleep(0.2)
            self.fail("PostgreSQL did not restart")

        try:
            for maximum, required, hosts in (
                    (85, 82, []), (84, 82, []),
                    (126, 205, ["remote-a", "remote-b", "remote-c"]),
                    (208, 205, ["remote-a", "remote-b", "remote-c"]),
                    (207, 205, ["remote-a", "remote-b", "remote-c"]),
                    (126, 123, ["versiond", "versiond2", "remote-a", "remote-a"])):
                capacity(maximum)
                endpoints.write_text(json.dumps([dict(id=str(i), host=host) for i, host in enumerate(hosts)]))
                scenario_env = dict(env, VERSIOND_POOL_ENDPOINTS_FILE=str(endpoints)) if hosts else env
                result = subprocess.run([str(directory / "update-devshard.sh"), "--check"], env=scenario_env,
                                        text=True, capture_output=True, timeout=90)
                if maximum - 3 >= required:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn(f"budget {required} fits {maximum - 3}", result.stdout)
                else:
                    self.assert_failed(result, f"need {required}, available {maximum - 3}")
                self.assertFalse((directory / "state/pending").exists())
                self.assertNotIn("Step:", result.stdout)
        finally:
            capacity(100)

    def test_local_deployment_lock(self):
        with (self.join / ".gonka-deployment.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assert_failed(self.check(lock_path=self.join / ".gonka-deployment.lock"), "another deployment operation holds")

    def test_reference_without_devshard_schema(self):
        empty = self.root / "empty-reference.env"
        empty.write_text(self.reference.read_text() + "PGDATABASE=postgres\n")
        self.assert_failed(self.check(reference=empty), "cannot read the reference PostgreSQL")

    def test_reference_requires_explicit_settings(self):
        missing = self.root / "missing-reference.env"
        missing.write_text("PGDATABASE=reference\n")
        self.assert_failed(self.check(reference=missing), "must set PGHOST explicitly")
        forbidden = self.root / "forbidden-reference.env"
        forbidden.write_text(self.reference.read_text() + "PGSERVICE=unexpected\n")
        self.assert_failed(self.check(reference=forbidden), "unsupported PGSERVICE")

    def test_mutually_exclusive_modes(self):
        for extra in (("--check",), ("--dry-run",), ("--topology", "ha")):
            with self.subTest(extra=extra):
                self.assert_failed(self.check(extra=extra), "cannot be combined")


if __name__ == "__main__":
    unittest.main(verbosity=2)
