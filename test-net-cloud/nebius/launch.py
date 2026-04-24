import os
import shutil
import hashlib
import urllib.request
import zipfile
import subprocess
import json
import re
import time
import argparse
from pathlib import Path
from types import SimpleNamespace
from dataclasses import dataclass


@dataclass
class AccountKey:
    """Data class to hold account key information"""
    address: str
    pubkey: str
    name: str


CUSTOM_BASE_DIR = os.environ.get("TESTNET_BASE_DIR", None)
BASE_DIR = Path(CUSTOM_BASE_DIR) if CUSTOM_BASE_DIR else Path(os.environ["HOME"]).absolute()
GENESIS_VAL_NAME = "testnet-genesis"
GONKA_REPO_DIR = BASE_DIR / "gonka"
DEPLOY_DIR = GONKA_REPO_DIR / "deploy/join"
COLD_KEY_NAME = "gonka-account-key"

INFERENCED_BINARY = SimpleNamespace(
    zip_file=BASE_DIR / "inferenced-linux-amd64.zip",
    url="https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.11/inferenced-linux-amd64.zip",
    checksum="cdbe18342dad90ea7ad370ae9b739c3488ac1fc60bccaf419b3d0b538bf01ac0",
    path=BASE_DIR / "inferenced",
)

INFERENCED_STATE_DIR = BASE_DIR / ".inference"

def load_config_from_env(hf_home: str = None):
    """Load configuration from environment variables, with defaults"""
    default_config = {
        "KEY_NAME": "genesis",
        "KEYRING_PASSWORD": "12345678",
        "API_PORT": "8000",
        "PUBLIC_URL": "http://89.169.111.79:8000",
        "P2P_EXTERNAL_ADDRESS": "tcp://89.169.111.79:5000",
        "ACCOUNT_PUBKEY": "", # will be populated later
        "NODE_CONFIG": "./node-config.json",
        "HF_HOME": Path(hf_home) if hf_home else (Path(os.environ["HOME"]).absolute() / "hf-cache").__str__(),
        "SEED_API_URL": "http://89.169.111.79:8000",
        "SEED_NODE_RPC_URL": "http://89.169.111.79:26657",
        "DAPI_API__POC_CALLBACK_URL": "http://api:9100",
        "DAPI_CHAIN_NODE__URL": "http://node:26657",
        "DAPI_CHAIN_NODE__P2P_URL": "http://node:26656",
        "SEED_NODE_P2P_URL": "tcp://89.169.111.79:5000",
        "RPC_SERVER_URL_1": "http://89.169.111.79:26657",
        "RPC_SERVER_URL_2": "http://89.169.111.79:26657",
        "PORT": "8080",
        "INFERENCE_PORT": "5050",
        "KEYRING_BACKEND": "file",
        "SYNC_WITH_SNAPSHOTS": "true",
        "SNAPSHOT_INTERVAL": "200",
        "IS_TEST_NET": "true",
        "ETHEREUM_NETWORK": "sepolia",
        "BEACON_STATE_URL": "https://sepolia.checkpoint-sync.ethpandaops.io",
        # If set, import this mnemonic instead of generating a fresh key (join nodes)
        "COLD_KEY_MNEMONIC": "",
        # Comma-separated addresses to pre-fund in genesis — loaded from .env, default empty
        "PRE_FUND_ADDRESSES": "",
        # Amount granted to each PRE_FUND_ADDRESSES entry
        "PRE_FUND_AMOUNT": "150000000ngonka",
        # Docker Hub / ghcr.io credentials for pulling private images (e.g. versiond)
        "DOCKER_USERNAME": "",
        "DOCKER_TOKEN": "",
    }
    
    config = default_config.copy()
    overridden_vars = []
    
    print("Loading configuration from environment variables...")
    
    # Check each config key for environment variable override
    for key, default_value in default_config.items():
        env_value = os.environ.get(key)
        if env_value is not None:
            config[key] = env_value
            overridden_vars.append(f"{key}={env_value}")
            print(f"✓ Overridden {key}: {default_value} -> {env_value}")
        else:
            print(f"  Using default {key}: {default_value}")
    
    if overridden_vars:
        print(f"\nEnvironment variables overridden: {len(overridden_vars)}")
        for var in overridden_vars:
            print(f"  - {var}")
    else:
        print("\nNo environment variables overridden, using all defaults")
    
    return config


def load_dotenv_file(config: dict, dotenv_path: Path) -> None:
    """Merge key=value pairs from a .env file into config (overrides defaults, yields to real env vars)."""
    if not dotenv_path.exists():
        print(f"  No .env file found at {dotenv_path}, skipping")
        return
    print(f"Loading .env file from {dotenv_path}")
    with open(dotenv_path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, value = line.partition("=")
            key = key.strip()
            value = value.strip().strip('"').strip("'")
            # Real environment variables take precedence over .env file
            if os.environ.get(key) is not None:
                print(f"  .env: {key} skipped (overridden by real env var)")
            elif key in config:
                print(f"  .env: {key} = {value}")
                config[key] = value


# Load configuration from environment
custom_hf_home = os.environ.get("TESTNET_HF_HOME", None)
CONFIG_ENV = load_config_from_env(hf_home=custom_hf_home)
_DOTENV_PATH = Path(__file__).parent / ".env"
load_dotenv_file(CONFIG_ENV, _DOTENV_PATH)


def clean_state():
    # Preserve tmkms key across deploys so the consensus key stays stable.
    # Without this, every deploy generates a new key that doesn't match genesis.
    tmkms_key_src = DEPLOY_DIR / ".tmkms/secrets/priv_validator_key.softsign"
    tmkms_key_backup = BASE_DIR / "priv_validator_key.softsign.bak"
    if subprocess.run(["sudo", "test", "-f", str(tmkms_key_src)], capture_output=True).returncode == 0:
        subprocess.run(["sudo", "cp", str(tmkms_key_src), str(tmkms_key_backup)], check=True)
        subprocess.run(["sudo", "chmod", "644", str(tmkms_key_backup)], check=True)
        print(f"Preserved tmkms consensus key to {tmkms_key_backup}")

    if GONKA_REPO_DIR.exists():
        print(f"Removing {GONKA_REPO_DIR}")
        os.system(f"sudo rm -rf {GONKA_REPO_DIR}")
    
    if INFERENCED_BINARY.zip_file.exists():
        print(f"Removing {BASE_DIR / 'inferenced-linux-amd64.zip'}")
        os.system(f"sudo rm -f {BASE_DIR / 'inferenced-linux-amd64.zip'}")
    
    if INFERENCED_BINARY.path.exists():
        print(f"Keeping existing {BASE_DIR / 'inferenced'} (pre-built binary)")

    if INFERENCED_STATE_DIR.exists():
        print(f"Removing {INFERENCED_STATE_DIR}")
        os.system(f"sudo rm -rf {INFERENCED_STATE_DIR}")


def docker_compose_down():
    """Stop and remove all Docker containers from previous runs"""
    if DEPLOY_DIR.exists():
        print("Stopping any running Docker containers...")

        # Check if env-override file exists
        env_override_file = DEPLOY_DIR / "docker-compose.env-override.yml"
        compose_files = ["-f", "docker-compose.yml", "-f", "docker-compose.mlnode.yml"]
        if env_override_file.exists():
            compose_files.extend(["-f", "docker-compose.env-override.yml"])
        if (DEPLOY_DIR / "docker-compose.api-upgrade.yml").exists():
            compose_files.extend(["-f", "docker-compose.api-upgrade.yml"])
        
        try:
            # First try to stop containers gracefully
            result = subprocess.run(
                ["docker", "compose"] + compose_files + ["down"],
                cwd=DEPLOY_DIR,
                capture_output=True,
                text=True,
                timeout=30
            )
            if result.returncode == 0:
                print("Docker containers stopped successfully")
            else:
                print(f"Warning: docker compose down returned code {result.returncode}")
                if result.stderr:
                    print(f"Error output: {result.stderr}")
        except subprocess.TimeoutExpired:
            print("Warning: docker compose down timed out, trying force stop...")
            # Force stop if graceful shutdown times out
            compose_files_str = " ".join(compose_files)
            os.system(f"cd {DEPLOY_DIR} && docker compose {compose_files_str} down --timeout 5")
        except Exception as e:
            print(f"Warning: Error stopping Docker containers: {e}")
            # Try force stop as fallback
            compose_files_str = " ".join(compose_files)
            os.system(f"cd {DEPLOY_DIR} && docker compose {compose_files_str} down --timeout 5")
    else:
        print("Deploy directory doesn't exist, skipping Docker cleanup")


def clone_repo(branch="main"):
    if not GONKA_REPO_DIR.exists():
        print(f"Cloning {GONKA_REPO_DIR}")
        os.system(f"git clone https://github.com/gonka-ai/gonka.git {GONKA_REPO_DIR}")
        
        # Switch to the specified branch
        print(f"Switching to branch: {branch}")
        checkout_cmd = f"cd {GONKA_REPO_DIR} && git checkout {branch}"
        result = os.system(checkout_cmd)
        if result != 0:
            print(f"Warning: Failed to checkout branch {branch} (exit code: {result})")
            print("Continuing with the default branch...")
        else:
            print(f"Successfully switched to branch: {branch}")
    else:
        print(f"{GONKA_REPO_DIR} already exists")
        # Check if we need to switch branches
        current_branch_cmd = f"cd {GONKA_REPO_DIR} && git branch --show-current"
        current_branch = subprocess.run(current_branch_cmd, shell=True, capture_output=True, text=True)
        if current_branch.returncode == 0:
            current_branch_name = current_branch.stdout.strip()
            if current_branch_name != branch:
                print(f"Current branch is {current_branch_name}, switching to {branch}")
                switch_cmd = f"cd {GONKA_REPO_DIR} && git checkout {branch}"
                result = os.system(switch_cmd)
                if result != 0:
                    print(f"Warning: Failed to switch to branch {branch} (exit code: {result})")
                else:
                    print(f"Successfully switched to branch: {branch}")
            else:
                print(f"Already on branch: {branch}")


def clean_genesis_validators():
    """Clean up genesis/validators directory, keeping only template and our validator"""
    validators_dir = GONKA_REPO_DIR / "genesis/validators"
    
    if not validators_dir.exists():
        print(f"Validators directory doesn't exist: {validators_dir}")
        return
    
    print("Cleaning up genesis/validators directory...")
    
    # Get all subdirectories
    for item in validators_dir.iterdir():
        if item.is_dir():
            # Keep template and our validator directory
            if item.name == "template" or item.name == GENESIS_VAL_NAME:
                print(f"Keeping directory: {item.name}")
                continue
            
            # Remove other directories
            print(f"Removing directory: {item.name}")
            try:
                shutil.rmtree(item)
            except PermissionError:
                print(f"Permission denied removing {item}, trying with sudo...")
                os.system(f"sudo rm -rf {item}")
    
    print("Genesis validators cleanup completed!")


def create_state_dirs():
    template_dir = GONKA_REPO_DIR / "genesis/validators/template"
    my_dir = GONKA_REPO_DIR / f"genesis/validators/{GENESIS_VAL_NAME}"
    if not my_dir.exists():
        print(f"Creating {my_dir}")
        os.system(f"cp -r {template_dir} {my_dir}")
    else:
        print(f"{my_dir} already exists, contents: {list(my_dir.iterdir())}")


INFERENCED_CONTAINER_BINARY = BASE_DIR / "inferenced-container"


def build_inferenced_from_repo():
    """Build the inferenced binary from the cloned repo source.

    The Docker image builds a musl-linked binary (runs inside alpine containers).
    We save it to INFERENCED_CONTAINER_BINARY — a separate path from INFERENCED_BINARY
    (the glibc release binary used for host operations like key management and gentx).

    The node-binary-override mounts INFERENCED_CONTAINER_BINARY into the node container
    so the chain runs the correct branch version.
    """
    if not GONKA_REPO_DIR.exists():
        print("Repo not cloned yet, skipping inferenced build")
        return

    image_tag = "api:0.2.12-testnet"
    image_exists = os.system(f"docker image inspect {image_tag} >/dev/null 2>&1") == 0

    if not image_exists:
        print("Building api:0.2.12-testnet from repo source (~8 min)...")
        build_cmd = (
            f"DOCKER_BUILDKIT=1 docker build "
            f"--build-arg GOOS=linux --build-arg GOARCH=amd64 "
            f"-f decentralized-api/Dockerfile -t {image_tag} . "
            f"> /tmp/inferenced-build.log 2>&1"
        )
        # Build builder stage first (compile step — no network needed after cache)
        builder_cmd = (
            f"DOCKER_BUILDKIT=1 docker build "
            f"--build-arg GOOS=linux --build-arg GOARCH=amd64 "
            f"--target builder -t devshardd-builder "
            f"-f decentralized-api/Dockerfile . >> /tmp/inferenced-build.log 2>&1"
        )
        os.system(f"cd {GONKA_REPO_DIR} && {builder_cmd}")
        # Retry final stage up to 3x (apk network flakiness)
        for _retry in range(3):
            if os.system(f"cd {GONKA_REPO_DIR} && {build_cmd}") == 0:
                image_exists = True
                break
            print(f"  build attempt {_retry + 1}/3 failed (see /tmp/inferenced-build.log), retrying...")
        if not image_exists:
            print("Warning: api image build failed — gentx will use builder stage, node will use release binary")
    else:
        print(f"{image_tag} already exists, skipping build")

    # Extract inferenced (musl) for container use
    if not INFERENCED_CONTAINER_BINARY.exists():
        src = image_tag if image_exists else "devshardd-builder"
        if os.system(f"docker image inspect {src} >/dev/null 2>&1") == 0:
            if os.system(f"docker run --rm {src} cat /usr/bin/inferenced > {INFERENCED_CONTAINER_BINARY} && chmod +x {INFERENCED_CONTAINER_BINARY}") == 0:
                print(f"Extracted inferenced-container from {src}")
            else:
                print("Warning: could not extract inferenced-container")

    # Write api-upgrade override
    api_upgrade = DEPLOY_DIR / "docker-compose.api-upgrade.yml"
    api_upgrade.parent.mkdir(parents=True, exist_ok=True)
    api_upgrade.write_text("services:\n  api:\n    image: api:0.2.12-testnet\n")

    # Extract devshardd
    devshardd_dst = DEPLOY_DIR / "devshards/bin/devshardd"
    devshardd_dst.parent.mkdir(parents=True, exist_ok=True)
    if not devshardd_dst.exists():
        for src in ["api:0.2.12-testnet", "devshardd-builder"]:
            if os.system(f"docker image inspect {src} >/dev/null 2>&1") == 0:
                if os.system(f"docker run --rm {src} cat /app/decentralized-api/build/devshardd > {devshardd_dst} && chmod +x {devshardd_dst}") == 0:
                    print(f"Extracted devshardd from {src}")
                    break
        else:
            print("Warning: could not extract devshardd")

    devshardd_base = BASE_DIR / "devshardd"
    if devshardd_dst.exists() and not devshardd_base.exists():
        import shutil as _shutil2
        _shutil2.copy2(str(devshardd_dst), str(devshardd_base))
        os.chmod(str(devshardd_base), 0o755)


def install_inferenced():
    url = INFERENCED_BINARY.url
    inferenced_zip = INFERENCED_BINARY.zip_file
    checksum = INFERENCED_BINARY.checksum
    inferenced_path = INFERENCED_BINARY.path

    # Download if not exists
    if not inferenced_zip.exists():
        print(f"Downloading inferenced binary zip: {INFERENCED_BINARY.url}")
        max_retries = 5
        retry_delay = 5  # seconds
        for attempt in range(max_retries):
            try:
                urllib.request.urlretrieve(url, inferenced_zip)
                break
            except Exception as e:
                if attempt < max_retries - 1:
                    print(f"Download failed (attempt {attempt + 1}/{max_retries}): {e}")
                    print(f"Retrying in {retry_delay} seconds...")
                    time.sleep(retry_delay)
                else:
                    print(f"Download failed after {max_retries} attempts")
                    raise
    else:
        print(f"{inferenced_zip} already exists")
    
    # Verify checksum
    print(f"Verifying inferenced binary zip checksum...")
    with open(inferenced_zip, 'rb') as f:
        file_hash = hashlib.sha256(f.read()).hexdigest()
    
    if file_hash != checksum:
        raise ValueError(f"Checksum mismatch! Expected: {checksum}, Got: {file_hash}")
    else:
        print("Checksum verified successfully")
    
    # Extract if directory doesn't exist
    if not inferenced_path.exists():
        print(f"Extracting {inferenced_zip} to {BASE_DIR}")
        with zipfile.ZipFile(inferenced_zip, 'r') as zip_ref:
            zip_ref.extractall(BASE_DIR)
        
        # chmod +x $BASE_DIR/inferenced
        os.chmod(inferenced_path, 0o755)
    else:
        print(f"{inferenced_path} already exists")


def create_account_key():
    """Create (or recover) the cold account key using inferenced CLI.

    If KEY_MNEMONIC is set in CONFIG_ENV the key is recovered from that mnemonic
    instead of being freshly generated.  This lets join nodes use a pre-provisioned
    account that was already endowed in genesis.
    """
    inferenced_binary = INFERENCED_BINARY.path

    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")

    # Check if key already exists
    try:
        result = subprocess.run(
            [str(inferenced_binary), "keys", "list", "--keyring-backend", "file", "--home", str(INFERENCED_STATE_DIR)],
            capture_output=True, text=True, check=True,
        )
        if COLD_KEY_NAME in result.stdout:
            print(f"Account key '{COLD_KEY_NAME}' already exists")
            return
    except subprocess.CalledProcessError:
        pass

    mnemonic = CONFIG_ENV.get("COLD_KEY_MNEMONIC", "").strip()
    password = f"{CONFIG_ENV['KEYRING_PASSWORD']}\n"

    if mnemonic:
        print(f"Recovering account key '{COLD_KEY_NAME}' from mnemonic...")
        cmd = [
            str(inferenced_binary), "keys", "add", COLD_KEY_NAME,
            "--keyring-backend", "file", "--home", str(INFERENCED_STATE_DIR),
            "--recover",
        ]
        # --recover reads: mnemonic (then password twice)
        stdin_input = f"{mnemonic}\n{password}{password}"
    else:
        print(f"Generating new account key '{COLD_KEY_NAME}'...")
        cmd = [
            str(inferenced_binary), "keys", "add", COLD_KEY_NAME,
            "--keyring-backend", "file", "--home", str(INFERENCED_STATE_DIR),
        ]
        stdin_input = password + password  # password entered twice

    process = subprocess.Popen(
        cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    stdout, stderr = process.communicate(input=stdin_input)

    if process.returncode != 0:
        print(f"Error creating/recovering key: {stderr}")
        raise subprocess.CalledProcessError(process.returncode, "inferenced keys add")

    action = "recovered" if mnemonic else "created"
    print(f"Account key {action} successfully!")
    print(stdout)
    
    # Extract both address and pubkey from the output
    full_output = stdout + stderr if stderr else stdout
    
    # Extract address
    address_match = re.search(r"address:\s*([a-z0-9]+)", full_output)
    if not address_match:
        raise ValueError("Could not find address in output")
    address = address_match.group(1)
    
    # Extract pubkey
    pubkey_match = re.search(r"pubkey: '(.+?)'", full_output)
    if not pubkey_match:
        raise ValueError("Could not find pubkey in output")
    
    pubkey_json = pubkey_match.group(1)
    try:
        pubkey_data = json.loads(pubkey_json)
        pubkey = pubkey_data.get("key", "")
        if not pubkey:
            raise ValueError("Could not extract key from pubkey JSON")
    except json.JSONDecodeError:
        raise ValueError("Could not parse pubkey JSON")
    
    # Extract name
    name_match = re.search(r"name:\s*\"?([^\"]+)\"?", full_output)
    name = name_match.group(1) if name_match else CONFIG_ENV["KEY_NAME"]
    
    print(f"Extracted address: {address}")
    print(f"Extracted pubkey: {pubkey}")
    print(f"Extracted name: {name}")
    
    return AccountKey(address=address, pubkey=pubkey, name=name)


def create_config_env_file():
    """Create config.env file in deploy/join directory"""
    config_file_path = GONKA_REPO_DIR / "deploy/join/config.env"
    
    # Ensure the directory exists
    config_file_path.parent.mkdir(parents=True, exist_ok=True)
    
    # Create the config.env content
    config_content = []
    for key, value in CONFIG_ENV.items():
        config_content.append(f'export {key}="{value}"')
    
    # Write to file
    with open(config_file_path, 'w') as f:
        f.write('\n'.join(config_content))
    
    print(f"Created config.env at {config_file_path}")
    print("== config.env ==")
    print('\n'.join(config_content))
    print("=============")
    
    # Create docker-compose override for environment variables
    create_env_override()


def create_env_override():
    """Create docker-compose override file to inject IS_TEST_NET and CHAIN_ID into all containers"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    override_file = working_dir / "docker-compose.env-override.yml"
    
    is_test_net = CONFIG_ENV.get("IS_TEST_NET", "true")
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    
    override_content = f"""# Auto-generated environment override - do not commit
services:
  tmkms:
    environment:
      - IS_TEST_NET={is_test_net}
      - CHAIN_ID={chain_id}
  node:
    environment:
      - IS_TEST_NET={is_test_net}
      - CHAIN_ID={chain_id}
  api:
    environment:
      - IS_TEST_NET={is_test_net}
      - ENFORCED_MODEL_ID=Qwen/Qwen3-4B-Instruct-2507
      - ENFORCED_MODEL_ARGS=--enable-auto-tool-choice --tool-call-parser hermes --max-model-len 25000
  proxy:
    environment:
      - IS_TEST_NET={is_test_net}
      - DISABLE_GONKA_API=false
      - DISABLE_CHAIN_API=false
      - DISABLE_CHAIN_RPC=false
      - DISABLE_CHAIN_GRPC=false
  proxy-ssl:
    environment:
      - IS_TEST_NET={is_test_net}
  explorer:
    environment:
      - IS_TEST_NET={is_test_net}
"""
    
    with open(override_file, 'w') as f:
        f.write(override_content)
    
    print(f"Created environment override at {override_file}")
    return override_file


def get_compose_files_arg(include_mlnode=True):
    """Get docker compose -f arguments including env-override"""
    files = ["docker-compose.yml"]
    if include_mlnode:
        files.append("docker-compose.mlnode.yml")
    # Include tmkms chain_id override so tmkms never starts with gonka-mainnet
    if (DEPLOY_DIR / "docker-compose.tmkms-override.yml").exists():
        files.append("docker-compose.tmkms-override.yml")
    # Include api image override when present (placed manually for custom builds)
    if (DEPLOY_DIR / "docker-compose.api-upgrade.yml").exists():
        files.append("docker-compose.api-upgrade.yml")
    files.append("docker-compose.env-override.yml")

    args = []
    for f in files:
        args.extend(["-f", f])
    return " ".join(args)


def docker_login_ghcr():
    """Log in to Docker Hub (and ghcr.io) if credentials are provided in the environment.

    Set DOCKER_USERNAME and DOCKER_TOKEN in .env.
    """
    username = CONFIG_ENV.get("DOCKER_USERNAME", "").strip()
    token = CONFIG_ENV.get("DOCKER_TOKEN", "").strip()
    if not username or not token:
        print("DOCKER_USERNAME/DOCKER_TOKEN not set — skipping docker login (private images like versiond may fail to pull)")
        return
    for registry in ["ghcr.io", ""]:  # "" = docker.io default
        result = subprocess.run(
            ["docker", "login"] + ([registry] if registry else []) + ["-u", username, "--password-stdin"],
            input=token, capture_output=True, text=True
        )
        label = registry or "docker.io"
        if result.returncode == 0:
            print(f"docker login {label} succeeded")
        else:
            print(f"Warning: docker login {label} failed: {result.stderr.strip()[:80]}")


def pull_images():
    """Pull Docker images using docker compose"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    print(f"Pulling Docker images from {working_dir}")
    
    # Create the command to source config.env and run docker compose
    # We use bash -c to run both commands in sequence
    compose_files = get_compose_files_arg(include_mlnode=True)
    cmd = f"bash -c 'source {config_file} && docker compose {compose_files} pull --ignore-pull-failures'"
    
    # Retry logic for network instability
    max_retries = 3
    retry_delay = 10  # seconds
    
    for attempt in range(max_retries):
        # Run the command in the specified working directory
        try:
            result = subprocess.run(
                cmd,
                shell=True,
                cwd=working_dir,
                capture_output=True,
                text=True,
                timeout=300,  # 5 min max — cached images check quickly; avoids hangs on unavailable registries
            )
        except subprocess.TimeoutExpired:
            print(f"Warning: image pull timed out after 5 min (continuing with cached images)")
            return

        if result.returncode == 0:
            print("Docker images pulled successfully!")
            if result.stdout:
                print(result.stdout)
            break

        if attempt < max_retries - 1:
            print(f"Error pulling images (attempt {attempt + 1}/{max_retries}): {result.stderr}")
            print(f"Retrying in {retry_delay} seconds...")
            time.sleep(retry_delay)
        else:
            print(f"Warning: image pull failed after {max_retries} attempts (continuing with cached images): {result.stderr}")

    # Pull mlnode directly by image name — large image (~29GB) that regularly exceeds the
    # 5 min general pull timeout. Pulling by explicit tag avoids picking up whatever tag
    # the branch's docker-compose.mlnode.yml happens to specify (e.g. 3.0.13-alpha3).
    MLNODE_IMAGE = "ghcr.io/product-science/mlnode:3.0.12-post4"
    print(f"Pulling {MLNODE_IMAGE} directly (up to 30 min)...")
    try:
        result = subprocess.run(
            ["docker", "pull", MLNODE_IMAGE],
            capture_output=True,
            text=True,
            timeout=1800,
        )
        if result.returncode == 0:
            print(f"mlnode image {MLNODE_IMAGE} pulled successfully!")
        else:
            print(f"Warning: mlnode pull failed (continuing with cached image): {result.stderr[:200]}")
    except subprocess.TimeoutExpired:
        print("Warning: mlnode pull timed out after 30 min (continuing with cached image)")

    # Patch docker-compose.mlnode.yml to use the correct image regardless of branch.
    mlnode_compose = GONKA_REPO_DIR / "deploy/join/docker-compose.mlnode.yml"
    if mlnode_compose.exists():
        import re as _re
        content = mlnode_compose.read_text()
        patched = _re.sub(
            r'image:\s*ghcr\.io/product-science/mlnode:[^\s]+',
            f'image: {MLNODE_IMAGE}',
            content
        )
        if patched != content:
            mlnode_compose.write_text(patched)
            print(f"Patched docker-compose.mlnode.yml to use {MLNODE_IMAGE}")


def create_docker_compose_override(init_only=True, node_id=None):
    """Create a docker-compose override file for genesis initialization or runtime"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    
    if init_only:
        override_file = working_dir / "docker-compose.genesis-override.yml"
        override_content = f"""services:
  node:
    ports:
      - "26657:26657"
    environment:
      - INIT_ONLY=true
      - IS_GENESIS=true
      - COIN_DENOM=ngonka
      - CHAIN_ID={chain_id}
  proxy:
    environment:
      - DISABLE_GONKA_API=false
      - DISABLE_CHAIN_API=false
      - DISABLE_CHAIN_RPC=false
      - DISABLE_CHAIN_GRPC=false
"""
    else:
        override_file = working_dir / "docker-compose.runtime-override.yml"
        if not node_id:
            raise ValueError("node_id is required for runtime override")
        
        # Extract P2P external address from CONFIG_ENV
        p2p_external_address = CONFIG_ENV.get("P2P_EXTERNAL_ADDRESS", "")
        if not p2p_external_address:
            raise ValueError("P2P_EXTERNAL_ADDRESS not found in CONFIG_ENV")
        
        # Convert tcp://host:port to host:port format for seeds
        if p2p_external_address.startswith("tcp://"):
            p2p_address = p2p_external_address[6:]  # Remove "tcp://" prefix
        else:
            p2p_address = p2p_external_address

        # Putting just some dummy value!
        genesis_seeds = f"7ea21aa72f90556628eb7354ee2d3f75a4b6148e@10.1.2.3:5000"
        
        override_content = f"""services:
  node:
    ports:
      - "26657:26657"
    environment:
      - INIT_ONLY=false
      - IS_GENESIS=true
      - GENESIS_SEEDS={genesis_seeds}
      - COIN_DENOM=ngonka
      - CHAIN_ID={chain_id}
  proxy:
    environment:
      - DISABLE_GONKA_API=false
      - DISABLE_CHAIN_API=false
      - DISABLE_CHAIN_RPC=false
      - DISABLE_CHAIN_GRPC=false
"""
    
    with open(override_file, 'w') as f:
        f.write(override_content)
    
    print(f"Created docker-compose override at {override_file}")
    return override_file


def run_genesis_initialization():
    """Run the node container with genesis initialization settings"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    override_file = create_docker_compose_override()
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    print("Running genesis initialization...")
    print("This will initialize the node with INIT_ONLY=true and IS_GENESIS=true")
    
    # Create the command to source config.env and run docker compose with override
    compose_files = get_compose_files_arg(include_mlnode=True)
    cmd = f"bash -c 'source {config_file} && docker compose {compose_files} -f {override_file} run --rm node'"
    
    # Run the command in the specified working directory
    result = subprocess.run(
        cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    print("Genesis initialization completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    # Extract nodeId from output
    full_output = result.stdout + result.stderr if result.stderr else result.stdout
    node_id_match = re.search(r'nodeId:\s*([a-f0-9]+)', full_output)
    if node_id_match:
        node_id = node_id_match.group(1)
        print(f"Extracted nodeId: {node_id}")
        # Store in CONFIG_ENV for potential future use
        CONFIG_ENV["NODE_ID"] = node_id
    else:
        print("Warning: Could not extract nodeId from output")
    
    if result.returncode != 0:
        print(f"Genesis initialization failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, cmd)
    
    print("Genesis initialization completed successfully!")


def extract_consensus_key():
    """Extract consensus key from tmkms container"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    print("Extracting consensus key from tmkms...")
    
    # First, start tmkms container in detached mode
    print("Starting tmkms container...")
    compose_files = get_compose_files_arg(include_mlnode=True)
    start_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} up -d tmkms'"
    
    start_result = subprocess.run(
        start_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    if start_result.returncode != 0:
        print(f"Error starting tmkms container: {start_result.stderr}")
        raise subprocess.CalledProcessError(start_result.returncode, start_cmd)
    
    print("Tmkms container started successfully")
    
    # Wait a moment for container to be ready
    time.sleep(2)
    
    # Now run the tmkms-pubkey command
    print("Running tmkms-pubkey command...")
    pubkey_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --entrypoint /bin/sh tmkms -c \"tmkms-pubkey\"'"
    
    pubkey_result = subprocess.run(
        pubkey_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    print("Consensus key extraction completed!")
    print("Output:")
    print("=" * 50)
    if pubkey_result.stdout:
        print(pubkey_result.stdout)
    if pubkey_result.stderr:
        print("Errors/Warnings:")
        print(pubkey_result.stderr)
    print("=" * 50)
    
    # Extract consensus key from output
    full_output = pubkey_result.stdout + pubkey_result.stderr if pubkey_result.stderr else pubkey_result.stdout
    consensus_key_match = re.search(r'([A-Za-z0-9+/=]{40,})', full_output)
    if consensus_key_match:
        consensus_key = consensus_key_match.group(1)
        print(f"Extracted consensus key: {consensus_key}")
        # Store in CONFIG_ENV for potential future use
        CONFIG_ENV["CONSENSUS_KEY"] = consensus_key
    else:
        print("Warning: Could not extract consensus key from output")
        print("Full output for debugging:")
        print(full_output)
        raise ValueError("Could not extract consensus key from output")
    
    if pubkey_result.returncode != 0:
        print(f"Consensus key extraction failed with return code: {pubkey_result.returncode}")
        raise subprocess.CalledProcessError(pubkey_result.returncode, pubkey_cmd)
    
    print("Consensus key extraction completed successfully!")
    return consensus_key


def get_or_create_warm_key(service="api"):
    """Create warm key using Docker compose and return AccountKey"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"

    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")

    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")

    compose_files = get_compose_files_arg(include_mlnode=True)

    # Check if the warm key already exists; if so, return it without re-creating.
    show_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} sh -lc \"printf \\\"%s\\\\n\\\" \\$KEYRING_PASSWORD | inferenced keys show \\$KEY_NAME --keyring-backend file\"'"
    show_result = subprocess.run(show_cmd, shell=True, cwd=working_dir, capture_output=True, text=True)
    if show_result.returncode == 0:
        full_output = show_result.stdout + show_result.stderr
        address_match = re.search(r"address:\s*([a-z0-9]+)", full_output)
        pubkey_match = re.search(r"pubkey: '(.+?)'", full_output)
        name_match = re.search(r"name:\s*\"?([^\"]+)\"?", full_output)
        if address_match and pubkey_match:
            address = address_match.group(1)
            pubkey_data = json.loads(pubkey_match.group(1))
            pubkey = pubkey_data.get("key", "")
            name = name_match.group(1) if name_match else CONFIG_ENV["KEY_NAME"]
            print(f"Warm key already exists: {address}")
            return AccountKey(address=address, pubkey=pubkey, name=name)

    print(f"Creating warm key for service: {service}")

    # Create the key
    add_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} sh -lc \"printf \\\"%s\\\\n%s\\\\n\\\" \\$KEYRING_PASSWORD \\$KEYRING_PASSWORD | inferenced keys add \\$KEY_NAME --keyring-backend file\"'"

    result = subprocess.run(
        add_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        print(f"Error creating key: {result.stderr}")
        raise subprocess.CalledProcessError(result.returncode, add_cmd)
    
    print("Warm key creation completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    # Extract both address and pubkey from output (same format as cold key)
    full_output = result.stdout + result.stderr if result.stderr else result.stdout
    
    # Extract address
    address_match = re.search(r"address:\s*([a-z0-9]+)", full_output)
    if not address_match:
        raise ValueError("Could not find address in warm key output")
    address = address_match.group(1)
    
    # Extract pubkey
    pubkey_match = re.search(r"pubkey: '(.+?)'", full_output)
    if not pubkey_match:
        raise ValueError("Could not find pubkey in warm key output")
    
    pubkey_json = pubkey_match.group(1)
    try:
        pubkey_data = json.loads(pubkey_json)
        pubkey = pubkey_data.get("key", "")
        if not pubkey:
            raise ValueError("Could not extract key from pubkey JSON")
    except json.JSONDecodeError:
        raise ValueError("Could not parse pubkey JSON")
    
    # Extract name
    name_match = re.search(r"name:\s*\"?([^\"]+)\"?", full_output)
    name = name_match.group(1) if name_match else CONFIG_ENV["KEY_NAME"]
    
    print(f"Extracted warm key address: {address}")
    print(f"Extracted warm key pubkey: {pubkey}")
    print(f"Extracted warm key name: {name}")
    
    return AccountKey(address=address, pubkey=pubkey, name=name)


def setup_genesis_file():
    """Copy genesis.json from Docker container to local state directory"""
    print("Setting up genesis.json file...")
    
    # Source and destination paths
    source_genesis = DEPLOY_DIR / ".inference/config/genesis.json"
    dest_dir = INFERENCED_STATE_DIR / "config"
    dest_genesis = dest_dir / "genesis.json"
    
    if not source_genesis.exists():
        raise FileNotFoundError(f"Source genesis.json not found at {source_genesis}")
    
    # Create destination directory if it doesn't exist
    dest_dir.mkdir(parents=True, exist_ok=True)
    
    # Copy the genesis.json file using sudo cp to avoid permission issues
    print(f"Copying {source_genesis} to {dest_genesis}")
    copy_result = os.system(f"sudo cp {source_genesis} {dest_genesis}")
    if copy_result != 0:
        raise RuntimeError(f"Failed to copy genesis.json file (exit code: {copy_result})")
    
    # Set permissions to 777
    print(f"Setting permissions on {dest_genesis}")
    chmod_result = os.system(f"sudo chmod 777 {dest_genesis}")
    if chmod_result != 0:
        raise RuntimeError(f"Failed to set permissions on genesis.json (exit code: {chmod_result})")
    
    print("Genesis.json setup completed successfully!")


def _add_genesis_account_by_address(address: str, amount: str = "150000000ngonka"):
    """Add a genesis account by raw address and amount."""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"

    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")

    print(f"Adding genesis account: {address}  amount: {amount}")

    compose_files = get_compose_files_arg(include_mlnode=True)
    genesis_cmd = (
        f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T node "
        f"sh -lc \"inferenced genesis add-genesis-account {address} {amount}\"'"
    )

    result = subprocess.run(genesis_cmd, shell=True, cwd=working_dir, capture_output=True, text=True)

    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print(result.stderr)
    print("=" * 50)

    if result.returncode != 0:
        raise subprocess.CalledProcessError(result.returncode, genesis_cmd)

    print(f"Genesis account added: {address}")


def add_genesis_account(account_key: AccountKey, amount: str = "150000000ngonka"):
    """Add genesis account using an AccountKey."""
    _add_genesis_account_by_address(account_key.address, amount)


def fund_distribution_module_account(community_pool_amount="120000000000000000"):
    """
    Fund the distribution module account for the community pool by directly editing genesis JSON.
    This sets both the bank balance AND the distribution module's community_pool field.
    """
    print(f"Funding distribution module account with {community_pool_amount}ngonka...")
    
    # Distribution module account address (standard across Cosmos SDK)
    distribution_address = "gonka1jv65s3grqf6v6jl3dp4t6c9t9rk99cd8h2rzwa"
    
    # Path to genesis file in local state
    genesis_file = INFERENCED_STATE_DIR / "config/genesis.json"
    
    if not genesis_file.exists():
        raise FileNotFoundError(f"Genesis file not found at {genesis_file}")
    
    # Read the genesis file
    with open(genesis_file, 'r') as f:
        genesis_data = json.load(f)
    
    # Add balance for distribution module account
    if 'bank' not in genesis_data['app_state']:
        genesis_data['app_state']['bank'] = {}
    
    if 'balances' not in genesis_data['app_state']['bank']:
        genesis_data['app_state']['bank']['balances'] = []
    
    # Check if distribution module balance already exists
    balance_exists = False
    for balance_entry in genesis_data['app_state']['bank']['balances']:
        if balance_entry['address'] == distribution_address:
            # Update existing balance
            balance_entry['coins'] = [
                {
                    "denom": "ngonka",
                    "amount": community_pool_amount
                }
            ]
            balance_exists = True
            print(f"Updated existing balance for distribution module")
            break
    
    if not balance_exists:
        # Add new balance entry
        genesis_data['app_state']['bank']['balances'].append({
            "address": distribution_address,
            "coins": [
                {
                    "denom": "ngonka",
                    "amount": community_pool_amount
                }
            ]
        })
        print(f"Added new balance entry for distribution module")
    
    # Update the supply to include the community pool amount
    if 'supply' in genesis_data['app_state']['bank']:
        for supply_entry in genesis_data['app_state']['bank']['supply']:
            if supply_entry['denom'] == 'ngonka':
                current_supply = int(supply_entry['amount'])
                new_supply = current_supply + int(community_pool_amount)
                supply_entry['amount'] = str(new_supply)
                print(f"Updated supply from {current_supply} to {new_supply}")
                break
    
    # Set the distribution module's community_pool field
    # This must match the bank balance to avoid "module balance does not match" panic
    if 'distribution' not in genesis_data['app_state']:
        genesis_data['app_state']['distribution'] = {}
    
    if 'fee_pool' not in genesis_data['app_state']['distribution']:
        genesis_data['app_state']['distribution']['fee_pool'] = {}
    
    # Set community_pool with decimal format (amount with .000000000000000000 suffix)
    genesis_data['app_state']['distribution']['fee_pool']['community_pool'] = [
        {
            "denom": "ngonka",
            "amount": f"{community_pool_amount}.000000000000000000"
        }
    ]
    print(f"Set distribution module community_pool field")
    
    # Write back to file with proper formatting
    with open(genesis_file, 'w') as f:
        json.dump(genesis_data, f, indent=2, separators=(',', ': '))
    
    print(f"Distribution module account funded successfully!")
    print(f"Address: {distribution_address}")
    print(f"Bank balance: {community_pool_amount}ngonka")
    print(f"Community pool: {community_pool_amount}.000000000000000000ngonka")


def _run_gentx_in_container(consensus_key: str, node_id: str, warm_key_address: str, chain_id: str) -> bool:
    """Run gentx inside the api docker image which has the correct chain binary.

    Falls back to this when the local release binary cannot validate the genesis
    (e.g. version mismatch between release binary and cloned branch).
    Returns True on success.
    """
    # Prefer the full api image; fall back to the builder stage which also has inferenced
    image = None
    for candidate in ["api:0.2.12-testnet", "devshardd-builder"]:
        if os.system(f"docker image inspect {candidate} >/dev/null 2>&1") == 0:
            image = candidate
            break
    if image is None:
        print("  no suitable docker image available for docker gentx")
        return False

    password = CONFIG_ENV['KEYRING_PASSWORD']
    gentx_args = (
        f"genesis gentx --keyring-backend file --home /root/.inference "
        f"{COLD_KEY_NAME} 1ngonka "
        f"--moniker {GENESIS_VAL_NAME} "
        f"--pubkey '{consensus_key}' "
        f"--ml-operational-address {warm_key_address} "
        f"--url {CONFIG_ENV['PUBLIC_URL']} "
        f"--chain-id {chain_id} "
        f"--node-id {node_id}"
    )
    cmd = (
        f"docker run --rm "
        f"-v {INFERENCED_STATE_DIR}:/root/.inference "
        f"{image} "
        f"sh -c \"printf '%s\\n%s\\n' '{password}' '{password}' | /usr/bin/inferenced {gentx_args}\""
    )
    print(f"Running gentx inside {image} container...")
    result = os.system(cmd)
    if result == 0:
        # Files written by the container are owned by root; fix so local binary can read them.
        os.system(f"sudo chown -R $(id -u):$(id -g) {INFERENCED_STATE_DIR} 2>/dev/null || true")
    return result == 0


def generate_gentx(account_key: AccountKey, consensus_key: str, node_id: str, warm_key_address: str, chain_id: str):
    """Generate genesis transaction.

    Tries the local release binary first. If it fails due to genesis version mismatch
    (unknown fields or validation errors from proto differences), falls back to running
    gentx inside the api docker container which has the correct chain binary.
    """
    print("Generating genesis transaction (gentx)...")

    password_input = f"{CONFIG_ENV['KEYRING_PASSWORD']}\n"
    gpath = INFERENCED_STATE_DIR / "config/genesis.json"

    gentx_cmd = [
        str(INFERENCED_BINARY.path),
        "genesis", "gentx",
        "--keyring-backend", "file",
        "--home", str(INFERENCED_STATE_DIR),
        COLD_KEY_NAME, "1ngonka",
        "--moniker", GENESIS_VAL_NAME,
        "--pubkey", consensus_key,
        "--ml-operational-address", warm_key_address,
        "--url", CONFIG_ENV["PUBLIC_URL"],
        "--chain-id", chain_id,
        "--node-id", node_id,
    ]

    # If the api docker image is available, use it directly — it has the correct chain
    # binary for the cloned branch and avoids all version-mismatch stripping issues.
    if os.system("docker image inspect api:0.2.12-testnet >/dev/null 2>&1") == 0:
        print("api:0.2.12-testnet image available — running gentx inside container (skipping local binary).")
        if _run_gentx_in_container(consensus_key, node_id, warm_key_address, chain_id):
            print("Docker gentx succeeded.")
            gentx_dir = INFERENCED_STATE_DIR / "config/gentx"
            genpart_dir = INFERENCED_STATE_DIR / "config/genparticipant"
            gentx_files = list(gentx_dir.glob("gentx-*.json")) if gentx_dir.exists() else []
            genpart_files = list(genpart_dir.glob("genparticipant-*.json")) if genpart_dir.exists() else []
            if gentx_files and genpart_files:
                return gentx_files[0].name, genpart_files[0].name
            return None, None
        raise RuntimeError("Docker gentx failed")

    # Fallback: local release binary with unknown-field auto-strip loop.
    process = subprocess.Popen(
        gentx_cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
    )
    stdout, stderr = process.communicate(input=password_input)

    for _attempt in range(20):
        if process.returncode == 0:
            break
        full_err = stdout + stderr
        unknown_match = re.search(r'unknown field "([^"]+)"', full_err)
        if not unknown_match:
            print(f"Gentx generation failed with return code: {process.returncode}\n{full_err[:300]}")
            raise subprocess.CalledProcessError(process.returncode, gentx_cmd)
        bad_field = unknown_match.group(1)
        print(f"Auto-stripping unknown genesis field '{bad_field}' (attempt {_attempt + 1})...")
        with open(gpath) as _f:
            _g = json.load(_f)
        def _rm_field(obj, field):
            if isinstance(obj, dict):
                obj.pop(field, None)
                for v in obj.values(): _rm_field(v, field)
            elif isinstance(obj, list):
                for i in obj: _rm_field(i, field)
        _rm_field(_g, bad_field)
        with open(gpath, "w") as _f:
            json.dump(_g, _f, indent=2)
        process = subprocess.Popen(
            gentx_cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
        )
        stdout, stderr = process.communicate(input=password_input)
    else:
        raise subprocess.CalledProcessError(process.returncode, gentx_cmd)

    print("Gentx completed!")
    print(stdout)
    if stderr:
        print(stderr)
    
    # Extract the generated file paths from output (check both stdout and stderr)
    full_output = stdout + stderr if stderr else stdout
    
    gentx_file_match = re.search(r'gentx-([a-f0-9]+)\.json', full_output)
    genparticipant_file_match = re.search(r'genparticipant-([a-f0-9]+)\.json', full_output)
    
    if gentx_file_match and genparticipant_file_match:
        gentx_file = f"gentx-{gentx_file_match.group(1)}.json"
        genparticipant_file = f"genparticipant-{genparticipant_file_match.group(1)}.json"
        print(f"Generated gentx file: {gentx_file}")
        print(f"Generated genparticipant file: {genparticipant_file}")
        return gentx_file, genparticipant_file
    else:
        print("Warning: Could not extract generated file names from output")
        print(f"Full output for debugging: {full_output}")
        return None, None


def collect_genesis_transactions():
    """Collect genesis transactions using local inferenced binary"""
    print("Collecting genesis transactions...")
    
    # Use the local inferenced binary
    inferenced_binary = INFERENCED_BINARY.path
    
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")
    
    # Prepare the collect-gentxs command
    collect_cmd = [
        str(inferenced_binary),
        "genesis", "collect-gentxs",
        "--home", str(INFERENCED_STATE_DIR),
        "--gentx-dir", (INFERENCED_STATE_DIR / "config" / "gentx").__str__()
    ]
    
    print(f"Running collect-gentxs command: {' '.join(collect_cmd)}")
    
    # Run the command
    result = subprocess.run(
        collect_cmd,
        capture_output=True,
        text=True
    )
    
    print("Collect genesis transactions completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    if result.returncode != 0:
        print(f"Collect genesis transactions failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, collect_cmd)
    
    print("Genesis transactions collected successfully!")


def patch_genesis_participants():
    """Process participant registrations using local inferenced binary"""
    print("Processing participant registrations...")
    
    # Use the local inferenced binary
    inferenced_binary = INFERENCED_BINARY.path
    
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")
    
    # Prepare the patch-genesis command
    patch_cmd = [
        str(inferenced_binary),
        "genesis", "patch-genesis",
        "--home", str(INFERENCED_STATE_DIR),
        "--genparticipant-dir", (INFERENCED_STATE_DIR / "config" / "genparticipant").__str__()
    ]
    
    print(f"Running patch-genesis command: {' '.join(patch_cmd)}")
    
    # Run the command
    result = subprocess.run(
        patch_cmd,
        capture_output=True,
        text=True
    )
    
    print("Patch genesis participants completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    if result.returncode != 0:
        print(f"Patch genesis participants failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, patch_cmd)
    
    print("Genesis participants patched successfully!")


def copy_genesis_back_to_docker():
    """Copy the updated genesis.json back to Docker container directory"""
    print("Copying updated genesis.json back to Docker container...")
    
    # Source and destination paths
    source_genesis = INFERENCED_STATE_DIR / "config/genesis.json"
    dest_genesis = DEPLOY_DIR / ".inference/config/genesis.json"
    
    if not source_genesis.exists():
        raise FileNotFoundError(f"Source genesis.json not found at {source_genesis}")
    
    # Copy the updated genesis.json back using sudo cp
    print(f"Copying {source_genesis} to {dest_genesis}")
    copy_result = os.system(f"sudo cp {source_genesis} {dest_genesis}")
    if copy_result != 0:
        raise RuntimeError(f"Failed to copy updated genesis.json back to Docker (exit code: {copy_result})")
    
    # Set permissions on the copied file
    print(f"Setting permissions on {dest_genesis}")
    chmod_result = os.system(f"sudo chmod 777 {dest_genesis}")
    if chmod_result != 0:
        raise RuntimeError(f"Failed to set permissions on updated genesis.json (exit code: {chmod_result})")
    
    print("Genesis.json copied back to Docker container successfully!")


def apply_genesis_overrides(overrides_file_path):
    """Apply genesis overrides from a JSON file, merging them into genesis.json"""
    print(f"Applying genesis overrides from {overrides_file_path}...")
    
    genesis_file = INFERENCED_STATE_DIR / "config/genesis.json"
    
    if not genesis_file.exists():
        raise FileNotFoundError(f"Genesis file not found at {genesis_file}")
    
    if not Path(overrides_file_path).exists():
        raise FileNotFoundError(f"Overrides file not found at {overrides_file_path}")
    
    # Read the genesis.json file
    with open(genesis_file, 'r') as f:
        genesis_data = json.load(f)
    
    # Read the overrides file
    with open(overrides_file_path, 'r') as f:
        overrides_data = json.load(f)
    
    # Merge the overrides into genesis data (deep merge)
    def deep_merge(target, source):
        """Deep merge source into target"""
        for key, value in source.items():
            if key in target and isinstance(target[key], dict) and isinstance(value, dict):
                deep_merge(target[key], value)
            else:
                target[key] = value
    
    # Apply the overrides
    deep_merge(genesis_data, overrides_data)
    
    # Write back to file with proper formatting
    with open(genesis_file, 'w') as f:
        json.dump(genesis_data, f, indent=2, separators=(',', ': '))
    
    print(f"Genesis overrides applied successfully from {overrides_file_path}!")


def fetch_genesis_from_seed():
    """Fetch genesis.json from seed node RPC and save to repo genesis/ directory"""
    seed_node_rpc_url = CONFIG_ENV.get("SEED_NODE_RPC_URL")
    if not seed_node_rpc_url:
        raise ValueError("SEED_NODE_RPC_URL not found in CONFIG_ENV")
    
    # RPC endpoint for genesis
    genesis_url = f"{seed_node_rpc_url}/genesis"
    
    print(f"Fetching genesis from {genesis_url}...")
    
    try:
        # Fetch genesis content
        with urllib.request.urlopen(genesis_url) as response:
            data = json.loads(response.read().decode())
        
        # Extract genesis from result
        if 'result' in data and 'genesis' in data['result']:
            genesis_content = data['result']['genesis']
        elif 'genesis' in data:
            genesis_content = data['genesis']
        else:
            raise ValueError(f"Could not find genesis content in response from {genesis_url}")
        
        # Destination path (repo genesis directory for Docker mount)
        dest_genesis = GONKA_REPO_DIR / "genesis/genesis.json"
        
        # Ensure directory exists
        dest_genesis.parent.mkdir(parents=True, exist_ok=True)
        
        # Save to file
        with open(dest_genesis, 'w') as f:
            json.dump(genesis_content, f, indent=2)
        
        print(f"Genesis fetched and saved successfully to {dest_genesis}")
        
        return genesis_content
        
    except Exception as e:
        print(f"Error fetching genesis from {genesis_url}: {e}")
        # Try fallback to status endpoint if genesis is too large
        status_url = f"{seed_node_rpc_url}/status"
        print(f"Checking node status at {status_url} to confirm chain ID...")
        try:
             with urllib.request.urlopen(status_url) as response:
                data = json.loads(response.read().decode())
                print("Node status check successful. Warning: Genesis file could not be downloaded via RPC (likely too large).")
                print("Please ensure you have manually copied the correct genesis.json if it differs from the repo default.")
        except Exception as status_e:
             print(f"Error checking node status: {status_e}")
             
        raise RuntimeError(f"Failed to fetch genesis: {e}")



def set_chain_id_in_genesis(chain_id):
    """Update valid chain_id in genesis.json"""
    print(f"Setting chain_id to {chain_id} in genesis.json...")
    
    genesis_file = INFERENCED_STATE_DIR / "config/genesis.json"
    
    if not genesis_file.exists():
        raise FileNotFoundError(f"Genesis file not found at {genesis_file}")
    
    with open(genesis_file, 'r') as f:
        genesis_data = json.load(f)
    
    genesis_data['chain_id'] = chain_id
    
    with open(genesis_file, 'w') as f:
        json.dump(genesis_data, f, indent=2, separators=(',', ': '))
    
    print(f"Set chain_id to {chain_id} successfully!")


def copy_final_genesis_to_repo():
    """Copy the finalized genesis.json to the genesis/ directory in the repo"""
    print("Copying finalized genesis.json to repository genesis/ directory...")
    
    # Source and destination paths
    source_genesis = INFERENCED_STATE_DIR / "config/genesis.json"
    dest_genesis = GONKA_REPO_DIR / "genesis/genesis.json"
    
    if not source_genesis.exists():
        raise FileNotFoundError(f"Source genesis.json not found at {source_genesis}")
    
    # Ensure the genesis directory exists
    dest_genesis.parent.mkdir(parents=True, exist_ok=True)
    
    # Copy the finalized genesis.json to the repo genesis/ directory
    print(f"Copying {source_genesis} to {dest_genesis}")
    copy_result = os.system(f"sudo cp {source_genesis} {dest_genesis}")
    if copy_result != 0:
        raise RuntimeError(f"Failed to copy finalized genesis.json to repo (exit code: {copy_result})")
    
    # Set permissions on the copied file
    print(f"Setting permissions on {dest_genesis}")
    chmod_result = os.system(f"sudo chmod 644 {dest_genesis}")
    if chmod_result != 0:
        raise RuntimeError(f"Failed to set permissions on repo genesis.json (exit code: {chmod_result})")
    
    print("Finalized genesis.json copied to repository successfully!")


def register_joining_participant(service="api", max_retries=5, retry_delay=30):
    """
    Register this node as a new participant in the existing network using Docker compose.
    Retries if the node is not ready yet.
    """
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    # Get required configuration values
    public_url = CONFIG_ENV.get("PUBLIC_URL")
    account_pubkey = CONFIG_ENV.get("ACCOUNT_PUBKEY")
    seed_api_url = CONFIG_ENV.get("SEED_API_URL")
    
    if not public_url:
        raise ValueError("PUBLIC_URL not found in CONFIG_ENV")
    if not account_pubkey:
        raise ValueError("ACCOUNT_PUBKEY not found in CONFIG_ENV")
    if not seed_api_url:
        raise ValueError("SEED_API_URL not found in CONFIG_ENV")
    
    print(f"Registering joining participant using service: {service}")
    
    # Build the command to run inside the container
    # NOTE! variable are getting renamed inside the container
    compose_files = get_compose_files_arg(include_mlnode=True)
    register_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} sh -lc \"inferenced register-new-participant \\$DAPI_API__PUBLIC_URL \\$ACCOUNT_PUBKEY --node-address \\$DAPI_CHAIN_NODE__SEED_API_URL\"'"
    
    print(f"Running command: {register_cmd}")
    
    for attempt in range(max_retries):
        result = subprocess.run(
            register_cmd,
            shell=True,
            cwd=working_dir,
            capture_output=True,
            text=True
        )
        
        print(f"Participant registration attempt {attempt + 1}/{max_retries}")
        print("Output:")
        print("=" * 50)
        if result.stdout:
            print(result.stdout)
        if result.stderr:
            print("Errors/Warnings:")
            print(result.stderr)
        print("=" * 50)
        
        if result.returncode == 0:
            print("Participant registration completed successfully!")
            return
        
        # Check if it's a connection error (node not ready yet)
        if "connection refused" in result.stderr.lower() or "not responding" in result.stderr.lower():
            if attempt < max_retries - 1:
                print(f"Node not ready yet. Retrying in {retry_delay} seconds...")
                time.sleep(retry_delay)
                continue
        
        # For other errors, fail immediately
        print(f"Participant registration failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, register_cmd)
    
    # All retries exhausted
    print(f"Participant registration failed after {max_retries} attempts")
    raise subprocess.CalledProcessError(result.returncode, register_cmd)


def submit_new_participant_tx(max_retries=5, retry_delay=30):
    """Register this node as participant via direct chain tx.

    Replaces the API-based register_joining_participant() flow which relies on a
    GET /v1/participants/{address} query path that is broken on this chain version.
    """
    inferenced_binary = INFERENCED_BINARY.path
    public_url = CONFIG_ENV.get("PUBLIC_URL")
    seed_api_url = CONFIG_ENV.get("SEED_API_URL")
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    keyring_password = CONFIG_ENV.get("KEYRING_PASSWORD", "12345678")
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"

    if not public_url:
        raise ValueError("PUBLIC_URL not found in CONFIG_ENV")
    if not seed_api_url:
        raise ValueError("SEED_API_URL not found in CONFIG_ENV")

    # Auto-fetch validator consensus key from the running node container
    compose_files = get_compose_files_arg(include_mlnode=False)
    status_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} exec -T node wget -qO- http://localhost:26657/status'"
    validator_key = ""
    status_result = subprocess.run(status_cmd, shell=True, cwd=working_dir, capture_output=True, text=True)
    if status_result.returncode == 0:
        try:
            validator_key = json.loads(status_result.stdout)["result"]["validator_info"]["pub_key"]["value"]
            print(f"Auto-fetched validator key: {validator_key}")
        except Exception as e:
            print(f"Warning: could not parse validator key from node status: {e}")

    cmd = [
        str(inferenced_binary),
        "tx", "inference", "submit-new-participant",
        public_url,
        "--from", COLD_KEY_NAME,
        "--keyring-backend", "file",
        "--home", str(INFERENCED_STATE_DIR),
        "--node", f"{seed_api_url}/chain-rpc/",
        "--chain-id", chain_id,
        "--gas", "2000000",
        "--output", "json",
        "-y",
    ]
    if validator_key:
        cmd.extend(["--validator-key", validator_key])

    print(f"Submitting participant tx: {' '.join(cmd)}")

    for attempt in range(max_retries):
        process = subprocess.Popen(
            cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        stdout, stderr = process.communicate(input=f"{keyring_password}\n")
        full_output = stdout + stderr

        print(f"Attempt {attempt + 1}/{max_retries}")
        print("=" * 50)
        if stdout:
            print(stdout)
        if stderr:
            print(stderr)
        print("=" * 50)

        # code 0 = accepted by mempool (JSON output: "code": 0 or YAML: "code: 0")
        try:
            tx_result = json.loads(stdout)
            if tx_result.get("code", -1) == 0:
                print("Participant tx submitted successfully!")
                time.sleep(10)
                return
        except Exception:
            pass
        # fallback string check for YAML output
        if '"code": 0' in full_output or '"code":0' in full_output or '\ncode: 0\n' in full_output or full_output.strip().startswith('code: 0'):
            print("Participant tx submitted successfully!")
            time.sleep(10)
            return

        # already registered is not a fatal error
        if "already exists" in full_output.lower() or "already registered" in full_output.lower():
            print("Participant already registered on chain, continuing.")
            return

        # transient errors — retry
        if any(e in full_output.lower() for e in ["connection refused", "not responding", "timeout", "unavailable"]):
            if attempt < max_retries - 1:
                print(f"Node not ready, retrying in {retry_delay}s...")
                time.sleep(retry_delay)
                continue

        print(f"Participant tx failed (attempt {attempt + 1}): {full_output[:200]}")
        if attempt < max_retries - 1:
            time.sleep(retry_delay)

    raise RuntimeError(f"submit-new-participant tx failed after {max_retries} attempts")


def inject_approved_devshard_versions_into_overrides(overrides_path: Path):
    """Patch the genesis-overrides file with the devshardd approved_versions entry.

    Called just before apply_genesis_overrides() so the approved_versions are baked
    into genesis from block 0.  The binary URL points to file-server:80/devshardd,
    the nginx container that serves ./devshards/bin/ within the docker network.
    The sha256 is computed from the actual binary built during this deploy.
    """
    devshardd_path = DEPLOY_DIR / "devshards/bin/devshardd"
    if not devshardd_path.exists():
        print("devshardd binary not found, skipping approved_versions injection")
        return

    with open(devshardd_path, "rb") as f:
        sha256 = hashlib.sha256(f.read()).hexdigest()

    if not overrides_path.exists():
        print(f"Overrides file not found at {overrides_path}, skipping approved_versions injection")
        return

    with open(overrides_path) as f:
        overrides = json.load(f)

    # Inject into devshard_escrow_params.approved_versions inside inference app_state
    def _set_approved(obj):
        if isinstance(obj, dict):
            if "devshard_escrow_params" in obj:
                obj["devshard_escrow_params"]["approved_versions"] = [{
                    "name": "v0.2.12",
                    "binary": "http://file-server:80/devshardd",
                    "sha256": sha256,
                }]
                return True
            for v in obj.values():
                if _set_approved(v):
                    return True
        return False

    if _set_approved(overrides):
        with open(overrides_path, "w") as f:
            json.dump(overrides, f, indent=2)
        print(f"Injected approved_versions into {overrides_path} (sha256={sha256[:16]}...)")
    else:
        print("Warning: devshard_escrow_params not found in overrides, approved_versions not injected")


def grant_key_permissions(warm_key_address: str):
    """
    Grant ML operations permissions to the warm key
    
    Args:
        warm_key_address: The address of the warm key to grant permissions to
    """
    print("Granting ML operations permissions...")
    
    # Get required configuration values
    seed_api_url = CONFIG_ENV.get("SEED_API_URL")
    keyring_password = CONFIG_ENV.get("KEYRING_PASSWORD")
    
    if not seed_api_url:
        raise ValueError("SEED_API_URL not found in CONFIG_ENV")
    if not keyring_password:
        raise ValueError("KEYRING_PASSWORD not found in CONFIG_ENV")
    
    # Build the command
    cmd = [
        str(INFERENCED_BINARY.path),
        "tx", "inference", "grant-ml-ops-permissions",
        COLD_KEY_NAME,  # The key name to grant permissions to
        warm_key_address,  # The warm key address
        "--from", COLD_KEY_NAME,
        "--keyring-backend", "file",
        "--home", str(INFERENCED_STATE_DIR),
        "--gas", "2000000",
        "--node", f"{seed_api_url}/chain-rpc/"
    ]
    
    print(f"Running command: {' '.join(cmd)}")
    
    try:
        # Run the command with password input
        process = subprocess.Popen(
            cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True
        )
        
        # Send the password twice (for signing and confirmation)
        password_input = f"{keyring_password}\n{keyring_password}\n"
        stdout, stderr = process.communicate(input=password_input)
        
        if process.returncode == 0:
            print("ML operations permissions granted successfully!")
            if stdout:
                print("Output:")
                print(stdout)
        else:
            print(f"Grant permissions failed with return code: {process.returncode}")
            if stdout:
                print("Output:")
                print(stdout)
            if stderr:
                print("Error:")
                print(stderr)
            raise subprocess.CalledProcessError(process.returncode, cmd)
            
    except Exception as e:
        print(f"Error granting ML operations permissions: {e}")
        raise


def start_docker_services(
    compose_files: list = None,
    services: list = None,
    additional_args: list = None
):
    """
    Start Docker services with flexible configuration
    
    Args:
        compose_files: List of docker-compose files to use (default: ["docker-compose.yml", "docker-compose.mlnode.yml"])
        services: List of specific services to start (default: None = all services)
        additional_args: Additional docker compose arguments (default: ["-d"])
    """
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    # Set defaults
    if compose_files is None:
        compose_files = ["docker-compose.yml", "docker-compose.mlnode.yml"]
    
    # Always include env-override file to inject IS_TEST_NET
    if "docker-compose.env-override.yml" not in compose_files:
        compose_files.append("docker-compose.env-override.yml")
    
    if additional_args is None:
        additional_args = ["-d"]
    
    # Build docker compose command
    cmd_parts = ["docker", "compose"]
    
    # Add compose files
    for file in compose_files:
        cmd_parts.extend(["-f", file])
    
    # Add up command
    cmd_parts.append("up")

    # Never pull during up — pull_images() already ran with --ignore-pull-failures.
    # This prevents a single unavailable image from blocking all services.
    cmd_parts.append("--pull=never")

    # Add services if specified
    if services:
        cmd_parts.extend(services)

    # Add additional arguments
    cmd_parts.extend(additional_args)
    
    # Build final command with config sourcing
    docker_cmd = " ".join(cmd_parts)
    start_cmd = f"bash -c 'source {config_file} && {docker_cmd}'"
    
    print(f"Starting Docker services...")
    print(f"Compose files: {compose_files}")
    if services:
        print(f"Services: {services}")
    print(f"Additional args: {additional_args}")
    print(f"Running command: {start_cmd}")
    
    result = subprocess.run(
        start_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    print("Docker services startup completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    if result.returncode != 0:
        # Compose up failed — likely a service has a missing private image (e.g. versiond).
        # Retry by starting each service individually so one missing image doesn't block all.
        print(f"Warning: compose up returned {result.returncode}, retrying service-by-service...")
        compose_prefix = " ".join(f"-f {f}" for f in compose_files)
        svc_list_cmd = f"bash -c 'source {config_file} && docker compose {compose_prefix} config --services'"
        svc_result = subprocess.run(svc_list_cmd, shell=True, cwd=working_dir, capture_output=True, text=True)
        services_to_start = svc_result.stdout.strip().split() if svc_result.returncode == 0 else []
        failed_svcs = []
        for svc in services_to_start:
            svc_cmd = f"bash -c 'source {config_file} && docker compose {compose_prefix} up --pull=never --no-deps -d {svc}'"
            r = subprocess.run(svc_cmd, shell=True, cwd=working_dir, capture_output=True, text=True)
            if r.returncode != 0:
                failed_svcs.append(svc)
                print(f"  [warn] {svc} could not start: {(r.stderr or r.stdout).strip()[:120]}")
        critical = ["node", "api"]
        if any(s in failed_svcs for s in critical):
            raise RuntimeError(f"Critical services failed to start: {[s for s in critical if s in failed_svcs]}")
        if failed_svcs:
            print(f"Warning: {failed_svcs} did not start (may need docker login to ghcr.io)")

    print("Docker services started successfully!")


def patch_tmkms_chain_id(chain_id: str):
    """Patch tmkms.toml to replace gonka-mainnet with the actual chain_id.

    The tmkms image ships with gonka-mainnet hardcoded. Signing votes with the
    wrong chain_id produces invalid signatures that are rejected by peers,
    causing consensus to deadlock as soon as join nodes become validators.
    Called by both genesis_route and join_route after tmkms has been initialised.
    """
    tmkms_toml = DEPLOY_DIR / ".tmkms/tmkms.toml"
    # On a fresh node tmkms writes the file on first start — wait up to 30s.
    for _ in range(10):
        if subprocess.run(["sudo", "test", "-f", str(tmkms_toml)], capture_output=True).returncode == 0:
            break
        time.sleep(3)
    if subprocess.run(["sudo", "test", "-f", str(tmkms_toml)], capture_output=True).returncode == 0:
        subprocess.run(["sudo", "sed", "-i", "s/gonka-mainnet/" + chain_id + "/g", str(tmkms_toml)], check=True)
        print(f"Patched tmkms.toml: gonka-mainnet -> {chain_id}")
        subprocess.run(["docker", "compose", "-f", "docker-compose.yml", "-f", "docker-compose.env-override.yml",
                        "restart", "tmkms"], cwd=str(DEPLOY_DIR), capture_output=True)
        print("Restarted tmkms with patched chain_id")
    else:
        print(f"Warning: tmkms.toml not found at {tmkms_toml}, skipping chain_id patch")


def genesis_route(account_key: AccountKey, chain_id: str):
    print("\n=== GENESIS MODE: Initializing genesis node ===")
    run_genesis_initialization()

    # Restore preserved tmkms consensus key if it exists, so the same key is
    # used across redeploys and genesis.json always matches what tmkms signs with.
    tmkms_key_backup = BASE_DIR / "priv_validator_key.softsign.bak"
    tmkms_key_dst = DEPLOY_DIR / ".tmkms/secrets/priv_validator_key.softsign"
    if subprocess.run(["sudo", "test", "-f", str(tmkms_key_backup)], capture_output=True).returncode == 0:
        subprocess.run(["sudo", "cp", str(tmkms_key_backup), str(tmkms_key_dst)], check=True)
        subprocess.run(["sudo", "chmod", "600", str(tmkms_key_dst)], check=True)
        print(f"Restored tmkms consensus key from {tmkms_key_backup}")

    patch_tmkms_chain_id(chain_id)

    add_genesis_account(account_key)

    # Endow any additional accounts listed in PRE_FUND_ADDRESSES (e.g. join node operators)
    pre_fund_addresses = [a.strip() for a in CONFIG_ENV.get("PRE_FUND_ADDRESSES", "").split(",") if a.strip()]
    pre_fund_amount = CONFIG_ENV.get("PRE_FUND_AMOUNT", "150000000ngonka")
    for addr in pre_fund_addresses:
        _add_genesis_account_by_address(addr, pre_fund_amount)

    consensus_key = extract_consensus_key()
    warm_key = get_or_create_warm_key()

    # Phase 3. GENTX and GENPARTICIPANT generation
    # Setup genesis.json file for local gentx generation
    setup_genesis_file()
    set_chain_id_in_genesis(chain_id)
    fund_distribution_module_account()
    # Apply overrides before gentx so BLS/other params pass validation
    local_overrides = BASE_DIR / "genesis-overrides.json"
    repo_overrides = GONKA_REPO_DIR / "test-net-cloud/nebius/genesis-overrides.json"
    pre_gentx_overrides = local_overrides if local_overrides.exists() else repo_overrides
    inject_approved_devshard_versions_into_overrides(pre_gentx_overrides)
    apply_genesis_overrides(pre_gentx_overrides)
    # Strip fields removed from proto that the genesis template still emits, or fields
    # unknown to the local gentx binary (which may be older than the chain binary).
    # Fields removed across PRs #1002, #1003, #1045; dispute_phase_duration_blocks added
    # in upgrade-v0.2.12 and unknown to the v0.2.11 release binary used for gentx.
    _removed_fields = [
        "top_reward_amount", "top_rewards", "top_reward_period", "top_reward_payouts",
        "top_reward_payouts_per_miner", "top_reward_max_duration", "top_reward_allowed_failure",
        "top_miner_poc_qualification", "top_miner_vesting_period",
        "subnet_escrow_params", "allowed_creator_addresses", "approved_versions",
    ]
    _gpath = INFERENCED_STATE_DIR / "config/genesis.json"
    if _gpath.exists():
        def _rm(obj, fields):
            if isinstance(obj, dict):
                for f in fields:
                    obj.pop(f, None)
                for v in obj.values(): _rm(v, fields)
            elif isinstance(obj, list):
                for i in obj: _rm(i, fields)
        with open(_gpath) as _f: _g = json.load(_f)
        _rm(_g, _removed_fields)
        with open(_gpath, "w") as _f: json.dump(_g, _f, indent=2)
        print(f"Stripped reserved fields from genesis: {_removed_fields}")
    # Generate gentx transaction
    node_id = CONFIG_ENV.get("NODE_ID", "")
    if not node_id:
        raise ValueError("NODE_ID not found in CONFIG_ENV")
    generate_gentx(account_key, consensus_key, node_id, warm_key.address, chain_id)

    # Phase 4. Genesis finalization
    collect_genesis_transactions()
    patch_genesis_participants()

    # Apply genesis overrides (includes denom_metadata and other configurations)
    # Check for local override file first (uploaded by prepare.sh), then fallback to repo
    local_overrides = BASE_DIR / "genesis-overrides.json"
    repo_overrides = GONKA_REPO_DIR / "test-net-cloud/nebius/genesis-overrides.json"
    
    if local_overrides.exists():
        print(f"Using local genesis overrides from {local_overrides}")
        genesis_overrides_path = local_overrides
    else:
        print(f"Using repo genesis overrides from {repo_overrides}")
        genesis_overrides_path = repo_overrides
        
    apply_genesis_overrides(genesis_overrides_path)
    
    set_chain_id_in_genesis(chain_id)

    copy_genesis_back_to_docker()
    copy_final_genesis_to_repo()


def join_route(account_key: AccountKey, chain_id: str):
    print("\n=== JOIN MODE: Joining existing network ===")

    # Try to fetch global genesis file from the seed node
    # This is critical if the chain ID has changed from default
    try:
        genesis_content = fetch_genesis_from_seed()

        # Verify Chain ID
        fetched_chain_id = genesis_content.get("chain_id", "unknown")
        if fetched_chain_id != chain_id:
            print(f"WARNING: Fetched genesis chain_id '{fetched_chain_id}' does not match desired '{chain_id}'")
            print(f"Using fetched chain_id '{fetched_chain_id}' as the source of truth.")
    except Exception as e:
        print(f"Warning: Could not fetch genesis from seed: {e}")
        print("Falling back to local repo genesis.json. Ensure it matches the network!")

    # Place 0.2.12 binary in cosmovisor so it doesn't fall back to 0.2.11 image binary.
    inferenced_src = BASE_DIR / "inferenced"
    cosmovisor_bin = DEPLOY_DIR / ".inference/cosmovisor/genesis/bin/inferenced"
    if inferenced_src.exists():
        subprocess.run(["sudo", "mkdir", "-p", str(cosmovisor_bin.parent)], check=True)
        subprocess.run(["sudo", "cp", str(inferenced_src), str(cosmovisor_bin)], check=True)
        subprocess.run(["sudo", "chmod", "+x", str(cosmovisor_bin)], check=True)
        print(f"Placed 0.2.12 binary in cosmovisor genesis bin path")

    start_docker_services(
        compose_files=["docker-compose.yml"],
        services=["tmkms", "node"],
        additional_args=["-d", "--no-deps"]
    )

    patch_tmkms_chain_id(chain_id)

    # Wait for node RPC to be ready before registering (up to 3 minutes).
    print("Waiting for node RPC to be ready...")
    seed_rpc = CONFIG_ENV.get("SEED_NODE_RPC_URL", "http://localhost:26657")
    node_rpc = "http://localhost:26657"
    for attempt in range(36):
        try:
            with urllib.request.urlopen(f"http://node:26657/status", timeout=3) as r:
                if r.status == 200:
                    print(f"Node RPC ready after {attempt * 5}s")
                    break
        except Exception:
            pass
        try:
            with urllib.request.urlopen(f"{seed_rpc}/status", timeout=3) as r:
                pass
        except Exception:
            pass
        time.sleep(5)
    else:
        print("Warning: node RPC not ready after 3 min, attempting registration anyway")

    # Get warm key for ML operations
    warm_key = get_or_create_warm_key()

    submit_new_participant_tx()
    grant_key_permissions(warm_key.address)


def parse_arguments():
    """Parse command-line arguments"""
    parser = argparse.ArgumentParser(
        description="Gonka testnet validator node setup script",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run in genesis mode (default)
  python launch.py
  python launch.py --mode genesis
  
  # Run in join mode
  python launch.py --mode join
  
  # Use specific branch
  python launch.py --branch nebius-test-net
  python launch.py --mode join --branch develop
  
  # Override configuration via environment variables
  export KEY_NAME="my-validator"
  export PUBLIC_URL="http://my-server.com:8000"
  python launch.py --mode genesis --branch nebius-test-net
        """
    )
    
    parser.add_argument(
        "--mode",
        choices=["genesis", "join"],
        default="genesis",
        help="Operation mode: 'genesis' for genesis node setup, 'join' for joining existing network (default: genesis)"
    )
    
    parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="Enable verbose output"
    )
    
    parser.add_argument(
        "--branch", "-b",
        default="main",
        help="Git branch to checkout after cloning (default: main)"
    )

    parser.add_argument(
        "--chainid",
        default="gonka-testnet",
        help="Chain ID to use for the network (default: gonka-testnet)"
    )
    
    return parser.parse_args()


def main():
    # Parse command-line arguments
    args = parse_arguments()
    
    # Store Chain ID in CONFIG_ENV so it permeates to config.env and Docker containers
    CONFIG_ENV["CHAIN_ID"] = args.chainid
    print(f"Using Chain ID: {args.chainid}")
    
    # Determine operation mode
    is_genesis = (args.mode == "genesis")
    
    print(f"Running in {'GENESIS' if is_genesis else 'JOIN'} mode")
    if args.verbose:
        print(f"Verbose mode enabled")
    
    if Path(os.getcwd()).absolute() != BASE_DIR:
        print(f"Changing directory to {BASE_DIR}")
        os.chdir(BASE_DIR)

    # Clean up any existing state
    docker_compose_down()  # Stop any running containers before cleanup
    clean_state()
    
    # Set up fresh environment
    clone_repo(args.branch)
    clean_genesis_validators()
    create_state_dirs()
    install_inferenced()

    # If the branch is newer than the release binary knows about, build from source.
    # This happens after a nuke teardown (binaries wiped) or when the repo branch
    # contains proto changes that the release binary doesn't understand.
    if args.branch not in ("main", "origin/main", "testnet/main", "origin/testnet/main"):
        print(f"Branch '{args.branch}' may have proto changes; rebuilding inferenced from source")
        build_inferenced_from_repo()

    # Create local 
    account_key = create_account_key()
    CONFIG_ENV["ACCOUNT_PUBKEY"] = account_key.pubkey
    create_config_env_file()

    # Mount the correct inferenced binary into the node container so cosmovisor uses it.
    # Prefer the musl container binary built from source; fall back to the release binary.
    # Both run fine inside the alpine-based node container.
    node_binary_path = INFERENCED_CONTAINER_BINARY if INFERENCED_CONTAINER_BINARY.exists() else BASE_DIR / "inferenced"
    if node_binary_path.exists():
        override = DEPLOY_DIR / "docker-compose.node-binary-override.yml"
        override.parent.mkdir(parents=True, exist_ok=True)
        override.write_text(f"services:\n  node:\n    volumes:\n      - {node_binary_path}:/usr/local/bin/inferenced\n      - {node_binary_path}:/usr/bin/inferenced\n")
        print(f"Written node binary override using {node_binary_path}")

    # Build versiond image from source (versioned/ dir in repo).
    # Tagged as ghcr.io/product-science/versiond:0.2.11 so the existing
    # docker-compose.yml service definition needs no changes.
    versiond_img = "ghcr.io/product-science/versiond:0.2.11"
    if os.system(f"docker image inspect {versiond_img} >/dev/null 2>&1") == 0:
        print(f"{versiond_img} already exists, skipping build")
    elif GONKA_REPO_DIR.exists() and (GONKA_REPO_DIR / "versioned/Dockerfile").exists():
        print("Building versiond from source...")
        if os.system(f"cd {GONKA_REPO_DIR}/versioned && docker build -t {versiond_img} . > /tmp/versiond-build.log 2>&1") == 0:
            print("versiond image built successfully")
        else:
            print("Warning: versiond build failed (see /tmp/versiond-build.log)")

    # Ensure devshards data dir exists (bin is created by build_inferenced_from_repo)
    (DEPLOY_DIR / "devshards/data").mkdir(parents=True, exist_ok=True)

    # Write a tmkms compose override with the correct chain_id.
    # The actual tmkms.toml patching happens in genesis_route() after init creates the file.
    chain_id = args.chainid
    tmkms_override = DEPLOY_DIR / "docker-compose.tmkms-override.yml"
    tmkms_override.parent.mkdir(parents=True, exist_ok=True)
    tmkms_override.write_text(f"services:\n  tmkms:\n    environment:\n      - CHAIN_ID={chain_id}\n")
    print(f"Written tmkms chain_id override ({chain_id})")

    # Clean up any containers that might have been started during setup
    docker_compose_down()  # Ensure clean state before starting new containers

    # Run the main processes
    docker_login_ghcr()
    pull_images()

    if is_genesis:
        genesis_route(account_key, args.chainid)
    else:
        join_route(account_key, args.chainid)

    # Phase 5. Start services
    if is_genesis:
        # Create runtime override for genesis nodes
        node_id = CONFIG_ENV.get("NODE_ID", "")
        if node_id:
            create_docker_compose_override(init_only=False, node_id=node_id)
            extra = [f for f in ["docker-compose.tmkms-override.yml", "docker-compose.node-binary-override.yml", "docker-compose.api-upgrade.yml"] if (DEPLOY_DIR / f).exists()]
            start_docker_services(
                compose_files=["docker-compose.yml", "docker-compose.mlnode.yml", "docker-compose.runtime-override.yml"] + extra
            )
        else:
            raise ValueError("NODE_ID not found in CONFIG_ENV")

        # Grant fee-grant from cold key to warm key so genesis node can submit PoC TXs.
        # join_route() does this automatically; genesis_route() registers via gentx so
        # the grant must be submitted after the chain is live.
        print("Waiting for genesis node to produce first block before granting key permissions...")
        for attempt in range(72):
            try:
                import json as _json
                with urllib.request.urlopen("http://localhost:26657/status", timeout=3) as r:
                    if r.status == 200:
                        data = _json.loads(r.read())
                        height = int(data.get("result", {}).get("sync_info", {}).get("latest_block_height", 0))
                        if height >= 1:
                            print(f"Node RPC ready at block {height} after {attempt * 5}s")
                            break
            except Exception:
                pass
            time.sleep(5)
        else:
            print("Warning: node RPC not at block 1 after 6 min, attempting fee grant anyway")
        warm_key = get_or_create_warm_key()
        grant_key_permissions(warm_key.address)
    else:
        extra = [f for f in ["docker-compose.tmkms-override.yml", "docker-compose.node-binary-override.yml", "docker-compose.api-upgrade.yml"] if (DEPLOY_DIR / f).exists()]
        start_docker_services(
            compose_files=["docker-compose.yml", "docker-compose.mlnode.yml"] + extra,
            additional_args=["-d"]
        )


if __name__ == "__main__":
    main()
