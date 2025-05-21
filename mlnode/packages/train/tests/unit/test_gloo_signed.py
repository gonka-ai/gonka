import pytest
import torch
import torch.distributed as dist
import torch.multiprocessing as mp

import subprocess
import os
import tempfile
import datetime


def generate_self_signed_cert_ed25519(cert_path, key_path, cn="dummy"):
    cmd_key = ["openssl", "genpkey", "-algorithm", "ED25519", "-out", key_path]
    subprocess.run(cmd_key, check=True)

    cmd_crt = [
        "openssl", "req", "-new", "-x509",
        "-key", key_path,
        "-out", cert_path,
        "-days", "365",
        "-subj", f"/CN={cn}"
    ]
    subprocess.run(cmd_crt, check=True)


def run_worker(rank, world_size, file_paths, scenario_name):
    print(f"\n[Scenario: {scenario_name}] Starting worker rank={rank}")

    os.environ["GLOO_DEVICE_TRANSPORT"] = "TCP_TLS"

    os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_PKEY"] = file_paths.get(f"rank{rank}_key", "")
    os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_CERT"] = file_paths.get(f"rank{rank}_crt", "")

    os.environ["GLOO_DEVICE_TRANSPORT_TCP_TLS_CA_FILE"] = file_paths["combined_ca"]

    print(f"  GLOO_DEVICE_TRANSPORT                = {os.environ.get('GLOO_DEVICE_TRANSPORT')}")
    print(f"  GLOO_DEVICE_TRANSPORT_TCP_TLS_PKEY   = {os.environ.get('GLOO_DEVICE_TRANSPORT_TCP_TLS_PKEY')}")
    print(f"  GLOO_DEVICE_TRANSPORT_TCP_TLS_CERT   = {os.environ.get('GLOO_DEVICE_TRANSPORT_TCP_TLS_CERT')}")
    print(f"  GLOO_DEVICE_TRANSPORT_TCP_TLS_CA_FILE= {os.environ.get('GLOO_DEVICE_TRANSPORT_TCP_TLS_CA_FILE')}")

    try:
        dist.init_process_group(
            backend="gloo",
            init_method="tcp://127.0.0.1:9999",
            world_size=world_size,
            rank=rank,
            timeout=datetime.timedelta(seconds=10)
        )

        tensor = torch.ones(1) * (rank + 1)
        dist.all_reduce(tensor, op=dist.ReduceOp.SUM)
        expected_value = sum(range(1, world_size + 1))

        if int(tensor.item()) == expected_value:
            print(f"[Scenario: {scenario_name}] Rank {rank}: Test passed! {tensor.item()} == {expected_value}")
        else:
            print(f"[Scenario: {scenario_name}] Rank {rank}: Test failed! {tensor.item()} != {expected_value}")

        dist.barrier()
        dist.destroy_process_group()

    except Exception as e:
        print(f"[Scenario: {scenario_name}] Rank {rank}: Caught exception:\n  {e}")
        raise


def run_scenario_normal_ed25519():
    print("\n=== SCENARIO (Ed25519): NORMAL (Both Certs Valid) ===")
    world_size = 2

    with tempfile.TemporaryDirectory() as tmpdir:
        file_paths = {}
        for r in range(world_size):
            crt_path = os.path.join(tmpdir, f"rank{r}.crt")
            key_path = os.path.join(tmpdir, f"rank{r}.key")
            generate_self_signed_cert_ed25519(crt_path, key_path, cn=f"Rank{r}")
            file_paths[f"rank{r}_crt"] = crt_path
            file_paths[f"rank{r}_key"] = key_path

        combined_ca_path = os.path.join(tmpdir, "combined_ca.pem")
        with open(combined_ca_path, "wb") as ca_out:
            for r in range(world_size):
                with open(file_paths[f"rank{r}_crt"], "rb") as crt_in:
                    ca_out.write(crt_in.read())
        file_paths["combined_ca"] = combined_ca_path

        mp.spawn(
            run_worker,
            args=(world_size, file_paths, "SCENARIO_ED25519"),
            nprocs=world_size
        )


def run_scenario_missing_cert():
    print("\n=== SCENARIO: MISSING CERT FOR RANK 1 ===")
    world_size = 2

    with tempfile.TemporaryDirectory() as tmpdir:
        file_paths = {}

        r0_crt = os.path.join(tmpdir, "rank0.crt")
        r0_key = os.path.join(tmpdir, "rank0.key")
        cmd_key = ["openssl", "genrsa", "-out", r0_key, "2048"]
        subprocess.run(cmd_key, check=True)
        cmd_crt = [
            "openssl", "req", "-new", "-x509",
            "-key", r0_key,
            "-out", r0_crt,
            "-days", "365",
            "-subj", "/CN=Rank0"
        ]
        subprocess.run(cmd_crt, check=True)

        file_paths["rank0_crt"] = r0_crt
        file_paths["rank0_key"] = r0_key

        combined_ca_path = os.path.join(tmpdir, "combined_ca.pem")
        with open(combined_ca_path, "wb") as ca_out:
            with open(file_paths["rank0_crt"], "rb") as crt_in:
                ca_out.write(crt_in.read())
        file_paths["combined_ca"] = combined_ca_path

        mp.spawn(
            run_worker,
            args=(world_size, file_paths, "SCENARIO_MISSING"),
            nprocs=world_size
        )


def run_scenario_invalid_cert():
    print("\n=== SCENARIO: INVALID CERT FOR RANK 1 ===")
    world_size = 2

    with tempfile.TemporaryDirectory() as tmpdir:
        file_paths = {}

        r0_crt = os.path.join(tmpdir, "rank0.crt")
        r0_key = os.path.join(tmpdir, "rank0.key")
        cmd_key = ["openssl", "genrsa", "-out", r0_key, "2048"]
        subprocess.run(cmd_key, check=True)
        cmd_crt = [
            "openssl", "req", "-new", "-x509",
            "-key", r0_key,
            "-out", r0_crt,
            "-days", "365",
            "-subj", "/CN=Rank0"
        ]
        subprocess.run(cmd_crt, check=True)

        file_paths["rank0_crt"] = r0_crt
        file_paths["rank0_key"] = r0_key

        r1_crt = os.path.join(tmpdir, "rank1.crt")
        r1_key = os.path.join(tmpdir, "rank1.key")
        cmd_key_r1 = ["openssl", "genrsa", "-out", r1_key, "2048"]
        subprocess.run(cmd_key_r1, check=True)
        cmd_crt_r1 = [
            "openssl", "req", "-new", "-x509",
            "-key", r1_key,
            "-out", r1_crt,
            "-days", "365",
            "-subj", "/CN=Rank1"
        ]
        subprocess.run(cmd_crt_r1, check=True)

        with open(r1_crt, "w") as f:
            f.write("THIS_IS_NOT_A_VALID_CERTIFICATE_FILE")

        file_paths["rank1_crt"] = r1_crt
        file_paths["rank1_key"] = r1_key

        combined_ca_path = os.path.join(tmpdir, "combined_ca.pem")
        with open(combined_ca_path, "wb") as ca_out:
            with open(r0_crt, "rb") as crt_in:
                ca_out.write(crt_in.read())
            with open(r1_crt, "rb") as crt_in:
                ca_out.write(crt_in.read())
        file_paths["combined_ca"] = combined_ca_path

        mp.spawn(
            run_worker,
            args=(world_size, file_paths, "SCENARIO_INVALID"),
            nprocs=world_size
        )


@pytest.mark.timeout(60)
def test_scenario_ed25519():
    run_scenario_normal_ed25519()


@pytest.mark.timeout(60)
def test_scenario_missing_cert():
    with pytest.raises(Exception):
        run_scenario_missing_cert()


@pytest.mark.timeout(60)
def test_scenario_invalid_cert():
    with pytest.raises(Exception):
        run_scenario_invalid_cert()