#!/usr/bin/env python3
"""Exercise --check-storage with Docker, BusyBox and real databases.

Only the versiond proof API is a stand-in; its addressed writes go to actual
PostgreSQL databases. No running deployment or fixed container names are used.
"""

import fcntl
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
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
        # Every check runs without a host psql, including tests of failure paths.
        cls.cli_bin = cls.root / "cli-bin"
        cls.cli_bin.mkdir()
        for tool in ("bash", "cat", "dirname", "docker", "flock", "jq", "sha256sum", "timeout"):
            (cls.cli_bin / tool).symlink_to(shutil.which(tool))
        assert shutil.which("psql", path=str(cls.cli_bin)) is None
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
        return run("docker", "exec", "-e", "PGPASSWORD=test-password", cls.pg,
                   "psql", "-h", "127.0.0.1", "-U", "postgres", "-d", database,
                   "-XwqAt", "-v", "ON_ERROR_STOP=1", "-c", statement).stdout.strip()

    @classmethod
    def configure(cls, member=0, *, mode="normal", databases=None):
        settings = dict(token=uuid.uuid4().hex, mode=mode,
                        databases=databases or ["reference", "reference"])
        (cls.fixture_dirs[member] / "settings.json").write_text(json.dumps(settings))

    def setUp(self):
        for i in range(len(self.members)):
            self.configure(i)
        self.sql("UPDATE devshard_storage_identity SET challenge = NULL")

    def check_command(self, *, members=None, reference=None, extra=(), lock_path=None, env_overrides=None):
        args = [str(self.join / "update-devshard.sh"), "--check-storage",
                "--reference-env", str(reference or self.reference)]
        for member in members or self.members[:1]:
            args.extend(["--container", member])
        # These inherited settings must never override the reference file.
        env = dict(os.environ, PGHOST="wrong.invalid", PGDATABASE="wrong",
                   PGPASSWORD="inherited-secret", PGSERVICE="wrong-service",
                   PGOPTIONS="-c search_path=wrong", GONKA_CONFIG_ENV=str(self.join / "config.env"),
                   PATH=str(self.cli_bin), GONKA_DEPLOYMENT_LOCK=str(self.root / "deployment.lock"))
        # Keep independent test fixtures and host shell startup files isolated.
        env.pop("BASH_ENV", None)
        env.pop("VERSIOND_STORAGE_CHECK_IMAGE", None)
        if lock_path:
            env["GONKA_DEPLOYMENT_LOCK"] = str(lock_path)
        env.update(env_overrides or {})
        return [*args, *extra], env

    def check(self, **kwargs):
        args, env = self.check_command(**kwargs)
        result = subprocess.run(args, capture_output=True, text=True,
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

    def test_explicit_ssl_disable(self):
        plain = self.root / "plain-reference.env"
        plain.write_text(self.reference.read_text() + "PGSSLMODE=disable\n")
        result = self.check(reference=plain)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Storage check passed", result.stdout)

    def test_unsupported_tls_settings(self):
        for key, value in (("PGSSLMODE", "verify-full"), ("PGSSLMODE", "require"),
                           ("PGSSLROOTCERT", "/missing/root.crt"),
                           ("PGSSLCERT", "/missing/client.crt"),
                           ("PGSSLKEY", "/missing/client.key")):
            with self.subTest(key=key, value=value):
                unsupported = self.root / "tls-reference.env"
                unsupported.write_text(self.reference.read_text() + f"{key}={value}\n")
                self.assert_failed(self.check(reference=unsupported), f"unsupported TLS setting {key}")
                self.assertEqual(self.sql("SELECT challenge FROM devshard_storage_identity"), "")

    def test_client_image_pull_failure(self):
        wrapper = self.root / "docker-pull-failure"
        wrapper.write_text(
            "#!/bin/bash\n"
            'if [[ $1 == image && $2 == inspect ]] || [[ $1 == pull ]]; then exit 1; fi\n'
            f"exec {shlex.quote(shutil.which('docker'))} \"$@\"\n")
        wrapper.chmod(0o755)
        self.assert_failed(self.check(env_overrides={"DOCKER_BIN": str(wrapper)}),
                           "cannot prepare PostgreSQL client image")
        self.assertEqual(self.sql("SELECT challenge FROM devshard_storage_identity"), "")

    def test_client_image_selection(self):
        observed = self.root / "client-images"
        wrapper = self.root / "docker-image-observer"
        wrapper.write_text(
            "#!/bin/bash\n"
            'args=("$@")\n'
            'if [[ $1 == image && $2 == inspect ]]; then\n'
            f'  printf "inspect %s\\n" "$3" >> {shlex.quote(str(observed))}\n'
            'elif [[ $1 == run ]]; then\n'
            '  while (($#)) && [[ $1 != --entrypoint ]]; do shift; done\n'
            '  shift 2\n'
            f'  printf "run %s\\n" "$1" >> {shlex.quote(str(observed))}\n'
            'elif [[ $1 == pull ]]; then exit 1\n'
            'fi\n'
            f'exec {shlex.quote(shutil.which("docker"))} "${{args[@]}}"\n')
        wrapper.chmod(0o755)
        for source in ("default", "server-setting", "shell", "reference"):
            with self.subTest(source=source):
                observed.write_text("")
                reference = self.root / "image-reference.env"
                reference.write_text(self.reference.read_text())
                settings = {"DOCKER_BIN": str(wrapper)}
                expected = "postgres:16-alpine"
                if source == "server-setting":
                    settings["DEVSHARD_POSTGRES_IMAGE"] = "server-only:must-not-be-used"
                elif source == "shell":
                    settings["VERSIOND_STORAGE_CHECK_IMAGE"] = expected = self.image
                elif source == "reference":
                    reference.write_text(reference.read_text() +
                                         f"VERSIOND_STORAGE_CHECK_IMAGE={self.image}\n")
                    expected = self.image
                result = self.check(reference=reference, env_overrides=settings)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(observed.read_text().splitlines(),
                                 [f"inspect {expected}", *[f"run {expected}"] * 3])

        # Choosing the client must leave the real Compose server image unchanged.
        env = dict(os.environ, KEY_NAME="test", DEVSHARD_POSTGRES_PASSWORD="test-password")
        env.pop("DEVSHARD_POSTGRES_IMAGE", None)
        compose = ["docker", "compose", "-f", str(SOURCE / "docker-compose.yml"),
                   "-f", str(SOURCE / "docker-compose.versiond.yml"), "config", "--format", "json"]
        before = json.loads(run(*compose, env=env).stdout)["services"]["devshard-postgres"]["image"]
        env["VERSIOND_STORAGE_CHECK_IMAGE"] = self.image
        after = json.loads(run(*compose, env=env).stdout)["services"]["devshard-postgres"]["image"]
        self.assertEqual(before, after)
        self.assertTrue(after.startswith("postgres@sha256:"), after)

    def test_client_pull_timeout_does_not_hold_lock(self):
        directory = self.root / "pull-timeout"
        directory.mkdir()
        started = directory / "started"
        wrapper = directory / "docker-wrapper"
        wrapper.write_text(
            "#!/bin/bash\n"
            'if [[ $1 == image && $2 == inspect ]]; then exit 1; fi\n'
            'if [[ $1 == pull ]]; then\n'
            f'  echo started > {shlex.quote(str(started))}\n'
            f'  exec {shlex.quote(shutil.which("sleep"))} 30\n'
            'fi\n'
            f'exec {shlex.quote(shutil.which("docker"))} "$@"\n')
        wrapper.chmod(0o755)
        timer = directory / "timeout"
        timer.write_text(
            '#!/bin/bash\n[[ $1 == --kill-after=* ]] && shift\nshift\n'
            f'exec {shlex.quote(shutil.which("timeout"))} --kill-after=1 2 "$@"\n')
        timer.chmod(0o755)
        lock_path = directory / "deployment.lock"
        args, env = self.check_command(lock_path=lock_path, env_overrides={
            "DOCKER_BIN": str(wrapper), "PATH": f"{directory}:{self.cli_bin}"})
        process = subprocess.Popen(args, env=env, text=True, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, start_new_session=True)
        try:
            deadline = time.monotonic() + 5
            while not started.exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(started.exists(), "image download was never started")
            with lock_path.open("a") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                stdout, stderr = process.communicate(timeout=5)
            self.assert_failed(subprocess.CompletedProcess(args, process.returncode, stdout, stderr),
                               "download failed or timed out")
            self.assertEqual(self.sql("SELECT challenge FROM devshard_storage_identity"), "")
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate()

    def test_client_cleanup_after_timeout(self):
        directory = self.root / "timeout-test"
        directory.mkdir()
        name_file = directory / "client-name"
        mounts_file = directory / "client-mounts"
        docker = shlex.quote(shutil.which("docker"))
        wrapper = directory / "docker-wrapper"
        wrapper.write_text(
            "#!/bin/bash\n"
            'if [[ $1 == run ]]; then\n'
            '  while (($#)); do\n'
            '    if [[ $1 == --name ]]; then name=$2; break; fi\n'
            '    shift\n'
            '  done\n'
            f'  {docker} run -d --rm --name "$name" --network none --read-only '
            '--entrypoint sleep postgres:16-alpine 300 >/dev/null || exit 1\n'
            f'  printf "%s\\n" "$name" > {shlex.quote(str(name_file))}\n'
            f'  {docker} inspect --format \'{{{{json .Mounts}}}}\' "$name" > {shlex.quote(str(mounts_file))}\n'
            f'  exec {shlex.quote(shutil.which("sleep"))} 300\n'
            'fi\n'
            f'exec {docker} "$@"\n')
        wrapper.chmod(0o755)
        # Kill the CLI without signalling its container: cleanup must remove it.
        timer = directory / "timeout"
        timer.write_text('#!/bin/bash\n[[ $1 == --foreground ]] && shift\nshift\n'
                         f'exec {shlex.quote(shutil.which("timeout"))} --signal=KILL 5 "$@"\n')
        timer.chmod(0o755)
        try:
            result = self.check(env_overrides={"DOCKER_BIN": str(wrapper),
                                               "PATH": f"{directory}:{self.cli_bin}"})
            self.assert_failed(result, "cannot read the reference PostgreSQL")
            self.assertTrue(name_file.exists(), "client container was never started")
            client = name_file.read_text().strip()
            inspected = subprocess.run(["docker", "inspect", client], capture_output=True, text=True)
            self.assertNotEqual(inspected.returncode, 0, "timed-out client container was retained")
            volumes = [m["Name"] for m in json.loads(mounts_file.read_text()) if m["Type"] == "volume"]
            self.assertTrue(volumes, "client image must exercise anonymous volume cleanup")
            for volume in volumes:
                inspected = subprocess.run(["docker", "volume", "inspect", volume], capture_output=True)
                self.assertNotEqual(inspected.returncode, 0, "client volume was retained")
            self.assertEqual(self.sql("SELECT challenge FROM devshard_storage_identity"), "")
        finally:
            if name_file.exists():
                subprocess.run(["docker", "rm", "-fv", name_file.read_text().strip()], capture_output=True)
            if mounts_file.exists():
                for mount in json.loads(mounts_file.read_text()):
                    if mount["Type"] == "volume":
                        subprocess.run(["docker", "volume", "rm", mount["Name"]], capture_output=True)

    def test_client_cleanup_after_signals(self):
        self.sql("CREATE DATABASE signal_reference")
        self.sql("CREATE VIEW devshard_storage_identity AS "
                 f"SELECT true AS singleton, '{self.identity}'::uuid AS identity FROM pg_sleep(30)",
                 "signal_reference")
        reference = self.root / "signal-reference.env"
        reference.write_text(self.reference.read_text() + "PGDATABASE=signal_reference\n")
        try:
            for signum in (signal.SIGTERM, signal.SIGINT):
                with self.subTest(signal=signum.name):
                    name_file = self.root / "signal-client-name"
                    name_file.unlink(missing_ok=True)
                    wrapper = self.root / "docker-signal-observer"
                    wrapper.write_text(
                        '#!/bin/bash\nargs=("$@")\n'
                        'if [[ $1 == run ]]; then\n'
                        '  while (($#)) && [[ $1 != --name ]]; do shift; done\n'
                        f'  printf "%s\\n" "$2" > {shlex.quote(str(name_file))}\n'
                        'fi\n'
                        f'exec {shlex.quote(shutil.which("docker"))} "${{args[@]}}"\n')
                    wrapper.chmod(0o755)
                    args, env = self.check_command(reference=reference,
                                                   env_overrides={"DOCKER_BIN": str(wrapper)})
                    process = subprocess.Popen(args, env=env, text=True, stdout=subprocess.PIPE,
                                               stderr=subprocess.PIPE, start_new_session=True)
                    client = None
                    volumes = []
                    try:
                        deadline = time.monotonic() + 8
                        while time.monotonic() < deadline:
                            if name_file.exists() and self.sql(
                                    "SELECT count(*) FROM pg_stat_activity "
                                    "WHERE datname='signal_reference' AND wait_event='PgSleep'") == "1":
                                break
                            time.sleep(0.05)
                        else:
                            self.fail("reference query never reached PostgreSQL")
                        client = name_file.read_text().strip()
                        state = json.loads(run("docker", "inspect", client).stdout)[0]
                        volumes = [m["Name"] for m in state["Mounts"] if m["Type"] == "volume"]
                        self.assertTrue(volumes)
                        os.killpg(process.pid, signum)
                        stdout, _ = process.communicate(timeout=4)
                        self.assertNotEqual(process.returncode, 0)
                        self.assertNotIn("Storage check passed", stdout)
                        inspected = subprocess.run(["docker", "inspect", client], capture_output=True)
                        self.assertNotEqual(inspected.returncode, 0, "interrupted client was retained")
                        for volume in volumes:
                            inspected = subprocess.run(["docker", "volume", "inspect", volume], capture_output=True)
                            self.assertNotEqual(inspected.returncode, 0, "interrupted client volume was retained")
                    finally:
                        if not client and name_file.exists():
                            client = name_file.read_text().strip()
                        if client:
                            subprocess.run(["docker", "rm", "-fv", client], capture_output=True)
                        if process.poll() is None:
                            os.killpg(process.pid, signal.SIGKILL)
                        process.communicate(timeout=15)
                        for volume in volumes:
                            subprocess.run(["docker", "volume", "rm", volume], capture_output=True)
                        self.sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                                 "WHERE datname='signal_reference'")
        finally:
            self.sql("DROP DATABASE signal_reference WITH (FORCE)")

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
