import os
import shutil
import hashlib
import urllib.request
import urllib.error
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


SCRIPT_DIR = Path(__file__).resolve().parent
CUSTOM_BASE_DIR = os.environ.get("TESTNET_BASE_DIR", None)
BASE_DIR = Path(CUSTOM_BASE_DIR).expanduser().resolve() if CUSTOM_BASE_DIR else SCRIPT_DIR
GENESIS_VAL_NAME = "testnet-genesis"
GONKA_REPO_DIR = BASE_DIR / "gonka"
DEPLOY_DIR = GONKA_REPO_DIR / "deploy/join"
COLD_KEY_NAME = "gonka-account-key"

INFERENCED_BINARY = SimpleNamespace(
    zip_file=BASE_DIR / "inferenced-linux-amd64.zip",
    url="https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.15/inferenced-linux-amd64.zip",
    checksum="4e506d74491bf2636591d4088f3eebd0ac4c33e739f83bc1cd56785482a9a7ca",
    path=BASE_DIR / "inferenced",
)

INFERENCED_STATE_DIR = BASE_DIR / ".inference"

def load_config_from_env(hf_home: str = None):
    """Load configuration from environment variables, with defaults"""
    default_config = {
        "KEY_NAME": "genesis",
        "KEYRING_PASSWORD": "12345678",
        "API_PORT": "8000",
        "PUBLIC_URL": "http://xj7-5.s.filfox.io:19244",
        "P2P_EXTERNAL_ADDRESS": "tcp://xj7-5.s.filfox.io:19243",
        "ACCOUNT_PUBKEY": "", # will be populated later
        "NODE_CONFIG": "./node-config.json",
        "HF_HOME": Path(hf_home) if hf_home else (Path(os.environ["HOME"]).absolute() / "hf-cache").__str__(),
        "SEED_API_URL": "http://xj7-5.s.filfox.io:19244",
        "SEED_NODE_RPC_URL": "http://xj7-5.s.filfox.io:19244/chain-rpc/",
        "DAPI_API__POC_CALLBACK_URL": "http://api:9100",
        "DAPI_CHAIN_NODE__URL": "http://node:26657",
        "DAPI_CHAIN_NODE__P2P_URL": "http://node:26656",
        "SEED_NODE_P2P_URL": "tcp://xj7-5.s.filfox.io:19243",
        "RPC_SERVER_URL_1": "http://xj7-5.s.filfox.io:19244/chain-rpc/",
        "RPC_SERVER_URL_2": "http://xj7-5.s.filfox.io:19244/chain-rpc/",
        "NODE_RPC_URL": "http://127.0.0.1:26657",
        "PORT": "8080",
        "INFERENCE_PORT": "5050",
        "KEYRING_BACKEND": "file",
        "SYNC_WITH_SNAPSHOTS": "true",
        "SNAPSHOT_INTERVAL": "200",
        "IS_TEST_NET": "true",
        "ETHEREUM_NETWORK": "sepolia",
        "BEACON_STATE_URL": "https://sepolia.checkpoint-sync.ethpandaops.io",
        "CHAIN_ID": "gonka-testnet",
        # Optional tx gas price (empty = zero fee on testnet where min_gas_price=0).
        "TX_GAS_PRICES": "",
        # Only checked when TX_GAS_PRICES is set (paid txs need funded cold key).
        "GRANT_MIN_SPENDABLE_NGONKA": "20000000000",
        "JOIN_FUND_WAIT_SECONDS": "600",
        "POSTGRES_HOST": "postgres",
        "POSTGRES_PORT": "5432",
        "POSTGRES_DB": "payloads",
        "POSTGRES_USER": "payloads",
        "POSTGRES_PASSWORD": "payloads",
        "BOUNTY_POOL_ENABLED": "true",
        "BOUNTY_POOL_IBC_DENOM": "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4",
        "BOUNTY_POOL_CHAIN_ID": "kava_2222-10",
        "BOUNTY_POOL_NAME": "USDT",
        "BOUNTY_POOL_SYMBOL": "USDT",
        "BOUNTY_POOL_DECIMALS": "6",
        "BOUNTY_POOL_AMOUNT": "1500000000000",
        "BOUNTY_POOL_COMMUNITY_SALE_LABEL": "community-sale-testnet-v1",
        "BOUNTY_POOL_GOV_AUTHORITY": "gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33",
        "WRAPPED_TOKEN_SETUP_ENABLED": "true",
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


# Load configuration from environment
custom_hf_home = os.environ.get("TESTNET_HF_HOME", None)
CONFIG_ENV = load_config_from_env(hf_home=custom_hf_home)


def clean_state():
    if GONKA_REPO_DIR.exists():
        print(f"Removing {GONKA_REPO_DIR}")
        os.system(f"sudo rm -rf {GONKA_REPO_DIR}")
    
    if BOUNTY_POOL_STATE_FILE.exists():
        print(f"Removing {BOUNTY_POOL_STATE_FILE}")
        os.system(f"sudo rm -f {BOUNTY_POOL_STATE_FILE}")

    if INFERENCED_BINARY.zip_file.exists():
        print(f"Removing {BASE_DIR / 'inferenced-linux-amd64.zip'}")
        os.system(f"sudo rm -f {BASE_DIR / 'inferenced-linux-amd64.zip'}")
    
    if INFERENCED_BINARY.path.exists():
        print(f"Removing {BASE_DIR / 'inferenced'}")
        os.system(f"sudo rm -f {BASE_DIR / 'inferenced'}")

    if INFERENCED_STATE_DIR.exists():
        print(f"Removing {INFERENCED_STATE_DIR}")
        os.system(f"sudo rm -rf {INFERENCED_STATE_DIR}")


def docker_compose_down():
    """Stop and remove all Docker containers from previous runs"""
    if DEPLOY_DIR.exists():
        print("Stopping any running Docker containers...")
        
        compose_files = ["-f", "docker-compose.yml", "-f", "docker-compose.mlnode.yml"]
        config_file = DEPLOY_DIR / "config.env"
        for override_name in (
            "docker-compose.postgres.yml",
            "docker-compose.env-override.yml",
            "docker-compose.rpc-override.yml",
            "docker-compose.runtime-override.yml",
            "docker-compose.genesis-override.yml",
        ):
            override_path = DEPLOY_DIR / override_name
            if not override_path.exists():
                continue
            # postgres.yml requires POSTGRES_* from config.env; skip it on pre-config teardown
            # so compose down still stops tmkms/node from a prior run.
            if override_name == "docker-compose.postgres.yml" and not config_file.exists():
                continue
            compose_files.extend(["-f", override_name])

        try:
            if config_file.exists():
                down_cmd = (
                    f"bash -c 'source {config_file} && docker compose "
                    + " ".join(compose_files)
                    + " down'"
                )
                result = subprocess.run(
                    down_cmd,
                    shell=True,
                    cwd=DEPLOY_DIR,
                    capture_output=True,
                    text=True,
                    timeout=30,
                )
            else:
                result = subprocess.run(
                    ["docker", "compose"] + compose_files + ["down"],
                    cwd=DEPLOY_DIR,
                    capture_output=True,
                    text=True,
                    timeout=30,
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


def _parse_cold_key_from_show_output(output: str) -> AccountKey:
    """Parse address and pubkey from `inferenced keys show` text or JSON output."""
    stripped = output.strip()
    if stripped.startswith("{"):
        payload = json.loads(stripped)
        pubkey_field = payload.get("pubkey")
        if isinstance(pubkey_field, dict):
            pubkey = pubkey_field.get("key", "")
        else:
            pubkey = pubkey_field or ""
        address = payload.get("address", "")
        name = payload.get("name", COLD_KEY_NAME)
        if not address or not pubkey:
            raise ValueError(f"Incomplete cold key data in output: {payload}")
        return AccountKey(address=address, pubkey=pubkey, name=name)

    address_match = re.search(r"address:\s*([a-z0-9]+)", stripped)
    if not address_match:
        raise ValueError("Could not find address in keys show output")
    pubkey_match = re.search(r"pubkey: '(.+?)'", stripped)
    if not pubkey_match:
        raise ValueError("Could not find pubkey in keys show output")
    pubkey_data = json.loads(pubkey_match.group(1))
    pubkey = pubkey_data.get("key", "")
    if not pubkey:
        raise ValueError("Could not extract key from pubkey JSON")
    name_match = re.search(r"name:\s*\"?([^\"]+)\"?", stripped)
    name = name_match.group(1) if name_match else COLD_KEY_NAME
    return AccountKey(address=address_match.group(1), pubkey=pubkey, name=name)


def load_existing_cold_account_key() -> AccountKey:
    """Load the cold account key from the host keyring (requires passphrase on stdin)."""
    inferenced_binary = INFERENCED_BINARY.path
    password = CONFIG_ENV.get("KEYRING_PASSWORD")
    if not password:
        raise ValueError("KEYRING_PASSWORD not found in CONFIG_ENV")

    cmd = [
        str(inferenced_binary),
        "keys",
        "show",
        COLD_KEY_NAME,
        "--keyring-backend",
        "file",
        "--home",
        str(INFERENCED_STATE_DIR),
        "--output",
        "json",
    ]
    process = subprocess.Popen(
        cmd,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    stdout, stderr = process.communicate(input=f"{password}\n")
    if process.returncode != 0:
        raise subprocess.CalledProcessError(
            process.returncode,
            cmd,
            output=stdout,
            stderr=stderr,
        )
    return _parse_cold_key_from_show_output(stdout)


def create_account_key():
    """Create account key using inferenced CLI"""
    inferenced_binary = INFERENCED_BINARY.path
    
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")
    
    # Check if key already exists
    try:
        result = subprocess.run(
            [str(inferenced_binary), "keys", "list", "--keyring-backend", "file", "--home", str(INFERENCED_STATE_DIR)],
            capture_output=True,
            text=True,
            check=True
        )
        if COLD_KEY_NAME in result.stdout:
            print(f"Account key '{COLD_KEY_NAME}' already exists, loading from keyring")
            return load_existing_cold_account_key()
    except subprocess.CalledProcessError:
        # Keyring might not exist yet, which is fine
        pass
    
    print("Creating account key 'gonka-account-key' with auto-generated passphrase...")
    
    # Execute the key creation command with automated password input
    # The password is "12345678" and needs to be entered twice
    password = f"{CONFIG_ENV['KEYRING_PASSWORD']}\n"  # \n for newline
    password_input = password + password  # Enter password twice
    
    process = subprocess.Popen([
        str(inferenced_binary), 
        "keys", 
        "add", 
        COLD_KEY_NAME, 
        "--keyring-backend", 
        "file",
        "--home",
        str(INFERENCED_STATE_DIR)
    ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    
    stdout, stderr = process.communicate(input=password_input)
    
    if process.returncode != 0:
        print(f"Error creating key: {stderr}")
        raise subprocess.CalledProcessError(process.returncode, "inferenced keys add")
    
    print("Account key created successfully!")
    print("Key details:")
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
    name = name_match.group(1) if name_match else COLD_KEY_NAME
    
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
    create_rpc_override()


def create_env_override():
    """Create docker-compose override file to inject IS_TEST_NET and CHAIN_ID into all containers"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    override_file = working_dir / "docker-compose.env-override.yml"
    
    is_test_net = CONFIG_ENV.get("IS_TEST_NET", "true")
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    ethereum_network = CONFIG_ENV.get("ETHEREUM_NETWORK", "sepolia")
    beacon_state_url = CONFIG_ENV.get(
        "BEACON_STATE_URL", "https://sepolia.checkpoint-sync.ethpandaops.io"
    )
    
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
  bridge:
    environment:
      - ETHEREUM_NETWORK={ethereum_network}
      - BEACON_STATE_URL={beacon_state_url}
"""
    
    with open(override_file, 'w') as f:
        f.write(override_content)
    
    print(f"Created environment override at {override_file}")
    return override_file


def create_rpc_override():
    """Publish Comet RPC on localhost so host-side launch.py can reach the node."""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    override_file = working_dir / "docker-compose.rpc-override.yml"
    override_content = """# Auto-generated RPC override - do not commit
services:
  node:
    ports:
      - "127.0.0.1:26657:26657"
"""
    with open(override_file, "w") as f:
        f.write(override_content)
    print(f"Created RPC override at {override_file}")
    return override_file


def standard_compose_files(include_mlnode=True):
    """Compose file list including generated overrides when present."""
    files = ["docker-compose.yml"]
    if include_mlnode:
        files.append("docker-compose.mlnode.yml")
    deploy_dir = GONKA_REPO_DIR / "deploy/join"
    for name in (
        "docker-compose.postgres.yml",
        "docker-compose.env-override.yml",
        "docker-compose.rpc-override.yml",
    ):
        path = deploy_dir / name
        if path.exists() and name not in files:
            files.append(name)
    return files


def ensure_compose_overrides(compose_files):
    """Append generated override files to an explicit compose file list."""
    files = list(compose_files)
    deploy_dir = GONKA_REPO_DIR / "deploy/join"
    for name in (
        "docker-compose.postgres.yml",
        "docker-compose.env-override.yml",
        "docker-compose.rpc-override.yml",
    ):
        if (deploy_dir / name).exists() and name not in files:
            files.append(name)
    return files


def get_compose_files_arg(include_mlnode=True):
    """Get docker compose -f arguments including env-override and rpc-override"""
    args = []
    for f in standard_compose_files(include_mlnode=include_mlnode):
        args.extend(["-f", f])
    return " ".join(args)


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
    cmd = f"bash -c 'source {config_file} && docker compose {compose_files} pull'"
    
    # Retry logic for network instability
    max_retries = 3
    retry_delay = 10  # seconds
    
    for attempt in range(max_retries):
        # Run the command in the specified working directory
        result = subprocess.run(
            cmd,
            shell=True,
            cwd=working_dir,
            capture_output=True,
            text=True
        )
        
        if result.returncode == 0:
            print("Docker images pulled successfully!")
            if result.stdout:
                print(result.stdout)
            return
        
        if attempt < max_retries - 1:
            print(f"Error pulling images (attempt {attempt + 1}/{max_retries}): {result.stderr}")
            print(f"Retrying in {retry_delay} seconds...")
            time.sleep(retry_delay)
        else:
            print(f"Error pulling images after {max_retries} attempts: {result.stderr}")
            raise subprocess.CalledProcessError(result.returncode, cmd)


def create_docker_compose_override(init_only=True, node_id=None):
    """Create a docker-compose override file for genesis initialization or runtime"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    
    if init_only:
        override_file = working_dir / "docker-compose.genesis-override.yml"
        # RPC host port is published by docker-compose.rpc-override.yml (127.0.0.1:26657).
        # Do not add 26657:26657 here — duplicate binds cause "address already in use".
        override_content = f"""services:
  node:
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
        
        # RPC host port is published by docker-compose.rpc-override.yml (127.0.0.1:26657).
        override_content = f"""services:
  node:
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
    
    # Heal broken partial-initialization state:
    # Some aborted runs leave config.toml without complete node state
    # (e.g. missing genesis.json or node_key.json), which makes init script
    # skip initialization and fail later. Force a clean re-init in that case.
    deploy_state_dir = DEPLOY_DIR / ".inference"
    init_flag = deploy_state_dir / ".node_initialized"
    config_toml = deploy_state_dir / "config/config.toml"
    genesis_file = deploy_state_dir / "config/genesis.json"
    node_key_file = deploy_state_dir / "config/node_key.json"
    stale_flag_state = init_flag.exists() and (not genesis_file.exists() or not node_key_file.exists())
    stale_config_state = config_toml.exists() and (not genesis_file.exists() or not node_key_file.exists())
    if stale_flag_state or stale_config_state:
        print("Detected stale init flag with missing node state; resetting deploy/join/.inference")
        os.system(f"sudo rm -rf {deploy_state_dir}")

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

    if pubkey_result.returncode != 0:
        print(f"Consensus key extraction failed with return code: {pubkey_result.returncode}")
        raise subprocess.CalledProcessError(pubkey_result.returncode, pubkey_cmd)
    
    # Extract consensus key from output
    full_output = pubkey_result.stdout + pubkey_result.stderr if pubkey_result.stderr else pubkey_result.stdout
    consensus_key_match = re.search(r'([A-Za-z0-9+/=]{40,})', full_output)
    if not consensus_key_match:
        print("Warning: Could not extract consensus key from output")
        print("Full output for debugging:")
        print(full_output)
        raise ValueError("Could not extract consensus key from output")

    consensus_key = consensus_key_match.group(1)
    print(f"Extracted consensus key: {consensus_key}")
    # Store in CONFIG_ENV for potential future use
    CONFIG_ENV["CONSENSUS_KEY"] = consensus_key
    
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
    keyring_password = CONFIG_ENV.get("KEYRING_PASSWORD")
    if not keyring_password:
        raise ValueError("KEYRING_PASSWORD not found in CONFIG_ENV")

    list_cmd = (
        f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} "
        "sh -lc \"inferenced keys list --keyring-backend file --output json\"'"
    )
    show_cmd = (
        f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} "
        "sh -lc \"printf \\\"%s\\\\n\\\" \\$KEYRING_PASSWORD | "
        "inferenced keys show \\$KEY_NAME --keyring-backend file --output json\"'"
    )
    add_cmd = (
        f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T {service} "
        "sh -lc \"printf \\\"%s\\\\n%s\\\\n\\\" \\$KEYRING_PASSWORD \\$KEYRING_PASSWORD | "
        "inferenced keys add \\$KEY_NAME --keyring-backend file\"'"
    )

    def parse_key_json(output: str) -> AccountKey:
        payload = json.loads(output)
        pubkey_field = payload.get("pubkey")
        if isinstance(pubkey_field, dict):
            pubkey = pubkey_field.get("key", "")
        else:
            pubkey = pubkey_field or ""
        address = payload.get("address", "")
        name = payload.get("name", CONFIG_ENV["KEY_NAME"])
        if not address or not pubkey:
            raise ValueError(f"Incomplete warm key data in output: {payload}")
        return AccountKey(address=address, pubkey=pubkey, name=name)

    def run_cmd(command: str, label: str, timeout_seconds: int = 120) -> subprocess.CompletedProcess:
        try:
            return subprocess.run(
                command,
                shell=True,
                cwd=working_dir,
                capture_output=True,
                text=True,
                timeout=timeout_seconds
            )
        except subprocess.TimeoutExpired as e:
            raise TimeoutError(
                f"{label} timed out after {timeout_seconds}s. "
                "This command must be non-interactive; check KEYRING_PASSWORD and container health."
            ) from e

    # First check key names to avoid interactive overwrite prompts.
    list_result = run_cmd(list_cmd, "warm key list")
    if list_result.returncode == 0:
        try:
            keys = json.loads(list_result.stdout or "[]")
            names = {
                entry.get("name", "")
                for entry in keys
                if isinstance(entry, dict)
            }
        except json.JSONDecodeError:
            names = set()
        if CONFIG_ENV.get("KEY_NAME") in names:
            show_result = run_cmd(show_cmd, "warm key show")
            if show_result.returncode != 0:
                print(f"Error reading existing warm key: {show_result.stderr}")
                raise subprocess.CalledProcessError(show_result.returncode, show_cmd)
            warm_key = parse_key_json(show_result.stdout.strip())
            print(f"Warm key already exists for service {service}, reusing: {warm_key.address}")
            return warm_key

    print(f"Creating warm key for service: {service}")
    add_result = run_cmd(add_cmd, "warm key add")
    if add_result.returncode != 0:
        print(f"Error creating key: {add_result.stderr}")
        raise subprocess.CalledProcessError(add_result.returncode, add_cmd)

    # Query the key after creation so parsing is stable across output formats.
    show_result = run_cmd(show_cmd, "warm key show")
    if show_result.returncode != 0:
        print(f"Error reading warm key after creation: {show_result.stderr}")
        raise subprocess.CalledProcessError(show_result.returncode, show_cmd)

    warm_key = parse_key_json(show_result.stdout.strip())
    print(f"Warm key ready for service {service}: {warm_key.address}")
    return warm_key


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


def add_genesis_account(account_key: AccountKey):
    """Add genesis account using the cold key address"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    config_file = working_dir / "config.env"
    
    if not working_dir.exists():
        raise FileNotFoundError(f"Working directory not found: {working_dir}")
    
    if not config_file.exists():
        raise FileNotFoundError(f"Config file not found: {config_file}")
    
    print(f"Adding genesis account for address: {account_key.address}")
    
    # Now run the genesis add-genesis-account command
    compose_files = get_compose_files_arg(include_mlnode=True)
    genesis_cmd = f"bash -c 'source {config_file} && docker compose {compose_files} run --rm --no-deps -T node sh -lc \"inferenced genesis add-genesis-account {account_key.address} 150000000ngonka\"'"

    print("Running genesis add-genesis-account command...")
    genesis_result = subprocess.run(
        genesis_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    print("Genesis account addition completed!")
    print("Output:")
    print("=" * 50)
    if genesis_result.stdout:
        print(genesis_result.stdout)
    if genesis_result.stderr:
        print("Errors/Warnings:")
        print(genesis_result.stderr)
    print("=" * 50)
    
    if genesis_result.returncode != 0:
        print(f"Genesis account addition failed with return code: {genesis_result.returncode}")
        raise subprocess.CalledProcessError(genesis_result.returncode, genesis_cmd)
    
    print("Genesis account added successfully!")


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


def fund_genesis_ibc_balance(address: str):
    """Add synthetic IBC token balance and supply to genesis for bounty pool bootstrap."""
    if CONFIG_ENV.get("BOUNTY_POOL_ENABLED", "true").lower() != "true":
        print("Bounty pool genesis funding skipped (BOUNTY_POOL_ENABLED is not true)")
        return

    denom = CONFIG_ENV.get("BOUNTY_POOL_IBC_DENOM", "")
    amount = CONFIG_ENV.get("BOUNTY_POOL_AMOUNT", "")
    if not denom or not amount:
        raise ValueError("BOUNTY_POOL_IBC_DENOM and BOUNTY_POOL_AMOUNT must be set")

    print(f"Funding genesis account {address} with {amount}{denom} for bounty pool...")

    genesis_file = INFERENCED_STATE_DIR / "config/genesis.json"
    if not genesis_file.exists():
        raise FileNotFoundError(f"Genesis file not found at {genesis_file}")

    with open(genesis_file, "r") as f:
        genesis_data = json.load(f)

    bank = genesis_data.setdefault("app_state", {}).setdefault("bank", {})
    balances = bank.setdefault("balances", [])

    balance_exists = False
    for balance_entry in balances:
        if balance_entry.get("address") == address:
            coins = balance_entry.setdefault("coins", [])
            coin_exists = False
            for coin in coins:
                if coin.get("denom") == denom:
                    coin["amount"] = str(int(coin.get("amount", 0)) + int(amount))
                    coin_exists = True
                    break
            if not coin_exists:
                coins.append({"denom": denom, "amount": amount})
            balance_exists = True
            break

    if not balance_exists:
        balances.append({
            "address": address,
            "coins": [{"denom": denom, "amount": amount}],
        })

    supply = bank.setdefault("supply", [])
    supply_exists = False
    for supply_entry in supply:
        if supply_entry.get("denom") == denom:
            supply_entry["amount"] = str(int(supply_entry.get("amount", 0)) + int(amount))
            supply_exists = True
            break
    if not supply_exists:
        supply.append({"denom": denom, "amount": amount})

    with open(genesis_file, "w") as f:
        json.dump(genesis_data, f, indent=2, separators=(",", ": "))

    print(f"Genesis IBC balance added: {amount} {denom} -> {address}")


BOUNTY_POOL_STATE_FILE = BASE_DIR / "bounty-pool-state.json"


def setup_bounty_pool():
    """Store community_sale, fund it with synthetic USDT, write bounty-pool-state.json."""
    if CONFIG_ENV.get("BOUNTY_POOL_ENABLED", "true").lower() != "true":
        print("Bounty pool post-start setup skipped (BOUNTY_POOL_ENABLED is not true)")
        return

    denom = CONFIG_ENV.get("BOUNTY_POOL_IBC_DENOM", "")
    amount = CONFIG_ENV.get("BOUNTY_POOL_AMOUNT", "")
    if BOUNTY_POOL_STATE_FILE.exists():
        try:
            with open(BOUNTY_POOL_STATE_FILE, "r") as f:
                state = json.load(f)
            contract = state.get("community_sale_address", "")
            if contract:
                rpc = CONFIG_ENV.get("NODE_RPC_URL", "http://127.0.0.1:26657")
                result = subprocess.run(
                    [
                        str(INFERENCED_BINARY.path),
                        "q", "bank", "balances", contract,
                        "--node", rpc, "-o", "json",
                    ],
                    capture_output=True,
                    text=True,
                )
                if result.returncode == 0:
                    balances = json.loads(result.stdout).get("balances", [])
                    for coin in balances:
                        if coin.get("denom") == denom and int(coin.get("amount", 0)) >= int(amount):
                            print(f"Bounty pool already ready at {contract}")
                            return
                raise RuntimeError(
                    f"bounty-pool-state.json exists but contract {contract} "
                    f"does not have expected {amount} {denom}"
                )
        except (json.JSONDecodeError, RuntimeError) as exc:
            if isinstance(exc, RuntimeError):
                raise
            print(f"Warning: could not parse {BOUNTY_POOL_STATE_FILE}, re-running setup")

    script = GONKA_REPO_DIR / "test-net-cloud/nebius/bridge/bridge-setup-community-sale.sh"
    if not script.exists():
        raise FileNotFoundError(f"Bounty pool setup script not found: {script}")

    print("Running bounty pool setup (community_sale store/instantiate/fund)...")
    env = os.environ.copy()
    env.update(CONFIG_ENV)
    env["CHAIN_ID"] = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    env["TESTNET_BASE_DIR"] = str(BASE_DIR)
    env["KEY_NAME"] = COLD_KEY_NAME

    result = subprocess.run(
        ["bash", str(script)],
        cwd=str(BASE_DIR),
        env=env,
        capture_output=True,
        text=True,
    )
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print(result.stderr)
    if result.returncode != 0:
        raise subprocess.CalledProcessError(result.returncode, script)

    if BOUNTY_POOL_STATE_FILE.exists():
        with open(BOUNTY_POOL_STATE_FILE, "r") as f:
            state = json.load(f)
        print("Bounty pool ready:")
        print(f"  contract: {state.get('community_sale_address')}")
        print(f"  denom: {state.get('ibc_denom')}")
        print(f"  amount: {state.get('amount')}")


def _query_account_sequence(address: str, node_rpc_url: str) -> int | None:
    """Return account sequence from chain, or None if account/query unavailable."""
    result = subprocess.run(
        [
            str(INFERENCED_BINARY.path),
            "q", "auth", "account", address,
            "--node", _normalize_node_rpc_url(node_rpc_url),
            "-o", "json",
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        return None
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError:
        return None
    account = payload.get("account") or {}
    if "base_account" in account:
        account = account["base_account"]
    seq = account.get("sequence")
    if seq is None:
        return None
    return int(seq)


def wait_for_cold_account_sequence_settled(
    address: str,
    node_rpc_url: str,
    timeout_seconds: int = 60,
) -> None:
    """Wait until cold-key sequence is stable on RPC (avoids post-bounty race)."""
    deadline = time.time() + timeout_seconds
    last_seq: int | None = None
    while time.time() < deadline:
        seq = _query_account_sequence(address, node_rpc_url)
        if seq is None:
            time.sleep(2)
            continue
        if last_seq is not None and seq == last_seq:
            time.sleep(2)
            if _query_account_sequence(address, node_rpc_url) == seq:
                print(f"Cold account sequence settled at {seq} ({node_rpc_url})")
                return
        last_seq = seq
        time.sleep(2)
    print(
        f"Warning: cold account sequence may still be changing "
        f"(last seen={last_seq}); proceeding with wrapped-token setup"
    )


def setup_wrapped_token_registration():
    """Upload wrapped_token.wasm and submit a governance proposal to register its code ID."""
    if CONFIG_ENV.get("WRAPPED_TOKEN_SETUP_ENABLED", "true").lower() != "true":
        print("Wrapped token registration skipped (WRAPPED_TOKEN_SETUP_ENABLED is not true)")
        return

    script = GONKA_REPO_DIR / "test-net-cloud/nebius/bridge/bridge-register-wrapped.sh"
    if not script.exists():
        raise FileNotFoundError(f"Wrapped token registration script not found: {script}")

    print("Running wrapped token registration (store WASM + governance proposal + vote)...")
    env = os.environ.copy()
    env.update(CONFIG_ENV)
    env["CHAIN_ID"] = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet")
    env["TESTNET_BASE_DIR"] = str(BASE_DIR)
    env["KEY_NAME"] = COLD_KEY_NAME
    env["KEYRING_PASSWORD"] = CONFIG_ENV.get("KEYRING_PASSWORD", "12345678")
    api_port = CONFIG_ENV.get("API_PORT", "8000")
    node_rpc_url = f"http://localhost:{api_port}/chain-rpc/"
    env["NODE_OPTS"] = f"--node {node_rpc_url}"

    try:
        cold_address = load_existing_cold_account_key().address
        wait_for_cold_account_sequence_settled(cold_address, node_rpc_url)
    except Exception as exc:
        print(f"Note: could not wait for cold account sequence: {exc}")

    result = subprocess.run(
        ["bash", str(script), "--use-repo"],
        cwd=str(BASE_DIR),
        env=env,
        capture_output=True,
        text=True,
    )
    combined_output = ""
    if result.stdout:
        print(result.stdout)
        combined_output += result.stdout
    if result.stderr:
        print(result.stderr)
        combined_output += result.stderr
    if result.returncode != 0:
        print("")
        print("=" * 72)
        print("WARNING: Wrapped token registration failed (genesis launch continues)")
        print("=" * 72)
        print("Bounty pool setup succeeded; only wrapped-token code ID registration failed.")
        print("Retry manually after checking the error above:")
        print(
            f"  cd {BASE_DIR} && "
            f"CHAIN_ID={env['CHAIN_ID']} TESTNET_BASE_DIR={BASE_DIR} "
            f"KEYRING_PASSWORD=$KEYRING_PASSWORD "
            f"bash {script} --use-repo"
        )
        print("Common causes:")
        print("  - account sequence mismatch after bounty pool (retry usually fixes it)")
        print("  - wrapped_token.wasm missing/empty (build: inference-chain/contracts/wrapped-token/build.sh)")
        print("  - cold key short on ngonka for gas/gov deposit")
        print(f"  - API/chain RPC not reachable at http://localhost:{api_port}/chain-rpc/")
        if combined_output.strip():
            print("")
            print("Last script output tail:")
            print("\n".join(combined_output.strip().splitlines()[-20:]))
        print("=" * 72)
        print("")
        return

    voting_period = "10m"
    try:
        overrides_path = GONKA_REPO_DIR / "test-net-cloud/nebius/genesis-overrides.json"
        local_overrides = BASE_DIR / "genesis-overrides.json"
        if local_overrides.exists():
            overrides_path = local_overrides
        with open(overrides_path, "r") as f:
            gov_params = json.load(f).get("app_state", {}).get("gov", {}).get("params", {})
            voting_period = gov_params.get("voting_period", voting_period)
    except (OSError, json.JSONDecodeError, AttributeError):
        pass

    print("")
    print("=" * 72)
    print("Wrapped token code ID registration: vote submitted (async)")
    print("=" * 72)
    print(
        f"The governance proposal was submitted and voted YES by the genesis key, "
        f"but launch.py does not wait for the voting period to finish "
        f"(currently {voting_period} in genesis-overrides.json)."
    )
    print(
        "Check proposal status asynchronously, for example:"
    )
    print(
        f"  {INFERENCED_BINARY.path} q gov proposals "
        f"--node {CONFIG_ENV.get('NODE_RPC_URL', 'http://127.0.0.1:26657')} -o json | jq '.proposals[-1]'"
    )
    print(
        "Once the proposal shows PROPOSAL_STATUS_PASSED, wrapped_token code ID is registered. "
        "USDC/USDT CW20 contracts are created later on the first bridge deposit (or via bridge-token-mint-sim.sh)."
    )
    print("=" * 72)
    print("")


def generate_gentx(account_key: AccountKey, consensus_key: str, node_id: str, warm_key_address: str, chain_id: str):
    """Generate genesis transaction using local inferenced binary"""
    print("Generating genesis transaction (gentx)...")
    
    # Use the local inferenced binary
    inferenced_binary = INFERENCED_BINARY.path
    
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")
    
    # Prepare the gentx command
    gentx_cmd = [
        str(inferenced_binary),
        "genesis", "gentx",
        "--keyring-backend", "file",
        "--home", str(INFERENCED_STATE_DIR),
        COLD_KEY_NAME, "1ngonka",
        "--moniker", GENESIS_VAL_NAME,
        "--pubkey", consensus_key,
        "--ml-operational-address", warm_key_address,
        "--url", CONFIG_ENV["PUBLIC_URL"],
        "--chain-id", chain_id,
        "--node-id", node_id
    ]
    
    print(f"Running gentx command: {' '.join(gentx_cmd)}")
    
    # Run the command with password input
    password_input = f"{CONFIG_ENV['KEYRING_PASSWORD']}\n"
    
    process = subprocess.Popen(
        gentx_cmd,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True
    )
    
    stdout, stderr = process.communicate(input=password_input)
    
    print("Gentx generation completed!")
    print("Output:")
    print("=" * 50)
    if stdout:
        print(stdout)
    if stderr:
        print("Errors/Warnings:")
        print(stderr)
    print("=" * 50)
    
    if process.returncode != 0:
        print(f"Gentx generation failed with return code: {process.returncode}")
        raise subprocess.CalledProcessError(process.returncode, gentx_cmd)
    
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


def register_joining_participant(
    cold_address: str = None,
    service="api",
    max_retries=5,
    retry_delay=30,
):
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
            if cold_address:
                # Seed proxy RPC (/chain-rpc/) is HTTP-only; use seed REST API here.
                wait_for_participant_on_seed(cold_address, timeout_seconds=120)
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


def _parse_ngonka_from_balance_payload(payload: dict) -> int:
    """Sum ngonka amounts from bank balance / spendable-balances JSON."""
    total = 0
    for key in ("balances", "spendable_balances"):
        for coin in payload.get(key) or []:
            if not isinstance(coin, dict):
                continue
            if coin.get("denom") == "ngonka":
                try:
                    total += int(coin.get("amount", "0"))
                except (TypeError, ValueError):
                    pass
    if total:
        return total
    # Seed REST /v2/accounts DTO
    if payload.get("denom") == "ngonka":
        try:
            return int(payload.get("balance", 0))
        except (TypeError, ValueError):
            return 0
    return 0


def query_spendable_balance_ngonka(address: str, node_rpc_url: str) -> int:
    """Return spendable ngonka for address (0 if account exists but empty)."""
    inferenced_binary = INFERENCED_BINARY.path
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")

    node = _normalize_node_rpc_url(node_rpc_url)
    for query_path in ("spendable-balances", "balances"):
        cmd = [
            str(inferenced_binary),
            "query",
            "bank",
            query_path,
            address,
            "--node",
            node,
            "-o",
            "json",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode == 0:
            try:
                return _parse_ngonka_from_balance_payload(json.loads(result.stdout))
            except json.JSONDecodeError:
                pass

    seed_api = (CONFIG_ENV.get("SEED_API_URL") or "").strip().rstrip("/")
    if seed_api:
        url = f"{seed_api}/v2/accounts/{address}"
        try:
            with urllib.request.urlopen(url, timeout=10) as resp:
                if resp.status == 200:
                    return _parse_ngonka_from_balance_payload(
                        json.loads(resp.read().decode("utf-8", errors="replace"))
                    )
        except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError, OSError):
            pass

    return 0


def _grant_funding_instructions(cold_address: str, warm_address: str, min_ngonka: int) -> str:
    chain_id = CONFIG_ENV.get("CHAIN_ID", "gonka-testnet-4")
    seed_api = (CONFIG_ENV.get("SEED_API_URL") or "<seed-api-url>").rstrip("/")
    seed_rpc = CONFIG_ENV.get("SEED_NODE_RPC_URL") or f"{seed_api}/chain-rpc/"
    amount = f"{min_ngonka}ngonka"
    return f"""
Join cold account needs GNK to pay grant-ml-ops-permissions gas (~11+ GNK).

1) On the funded GENESIS host, send coins to this join cold key:

   inferenced tx bank send <funded-key-name> {cold_address} {amount} \\
     --from <funded-key-name> --chain-id {chain_id} \\
     --node {seed_rpc} -y --gas auto --gas-prices 10ngonka

2) Verify balance:

   curl -s {seed_api}/v2/accounts/{cold_address} | jq .

3) Run grant on the join host:

   printf '%s\\n%s\\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | \\
     inferenced tx inference grant-ml-ops-permissions {COLD_KEY_NAME} {warm_address} \\
     --from {COLD_KEY_NAME} --keyring-backend file --home {INFERENCED_STATE_DIR} \\
     --chain-id {chain_id} --node tcp://127.0.0.1:26657 -y \\
     --gas auto --gas-adjustment 1.5
"""


def grant_tx_requires_funding() -> bool:
    """True when grant txs declare a non-zero gas price (mainnet-style)."""
    return bool((CONFIG_ENV.get("TX_GAS_PRICES") or "").strip())


def wait_for_cold_funding_before_grant(
    cold_address: str,
    warm_address: str,
    node_rpc_url: str,
):
    """Ensure cold key has enough ngonka for grant when TX_GAS_PRICES is set."""
    if not grant_tx_requires_funding():
        print(
            "TX_GAS_PRICES unset — grant uses zero/minimal fee (testnet min_gas_price=0). "
            "No cold-key funding required."
        )
        return

    min_ngonka = int(CONFIG_ENV.get("GRANT_MIN_SPENDABLE_NGONKA", "20000000000"))
    wait_seconds = int(CONFIG_ENV.get("JOIN_FUND_WAIT_SECONDS", "600"))

    balance = query_spendable_balance_ngonka(cold_address, node_rpc_url)
    if balance >= min_ngonka:
        print(f"Cold account balance OK: {balance} ngonka (need >= {min_ngonka})")
        return

    instructions = _grant_funding_instructions(cold_address, warm_address, min_ngonka)
    print(instructions)

    if wait_seconds <= 0:
        raise RuntimeError(
            f"Cold account {cold_address} has {balance} ngonka; need >= {min_ngonka} before grant. "
            "Fund from genesis (see instructions above) or set JOIN_FUND_WAIT_SECONDS>0 to poll."
        )

    print(
        f"Waiting up to {wait_seconds}s for cold account funding "
        f"({balance} / {min_ngonka} ngonka)..."
    )
    deadline = time.time() + wait_seconds
    last_progress = 0.0
    while time.time() < deadline:
        balance = query_spendable_balance_ngonka(cold_address, node_rpc_url)
        if balance >= min_ngonka:
            print(f"Cold account funded: {balance} ngonka")
            return
        now = time.time()
        if now - last_progress >= 30:
            remaining = int(deadline - now)
            print(f"  still unfunded ({balance} ngonka, {remaining}s left)...")
            last_progress = now
        time.sleep(15)

    raise TimeoutError(
        f"Cold account {cold_address} still has {balance} ngonka after {wait_seconds}s; "
        f"need >= {min_ngonka}. Fund from genesis and re-run grant."
    )


def _cold_address_from_keyring() -> str:
    """Best-effort cold address for error messages."""
    try:
        return load_existing_cold_account_key().address
    except Exception:
        return "<cold-address>"


def _verify_warm_account_after_grant(warm_key_address: str, node_rpc_url: str):
    """Best-effort check that warm has an auth account after grant (non-blocking)."""
    try:
        if auth_account_exists(warm_key_address, node_rpc_url):
            print(f"Warm auth account present: {warm_key_address}")
        else:
            print(
                f"Note: warm auth account not visible yet at {warm_key_address}; "
                "DAPI may still work once grant tx is indexed."
            )
    except (RuntimeError, subprocess.TimeoutExpired) as e:
        print(f"Note: could not verify warm auth account: {e}")


def grant_key_permissions(warm_key_address: str):
    """
    Grant ML operations permissions to the warm key

    Args:
        warm_key_address: The address of the warm key to grant permissions to
    """
    print("Granting ML operations permissions...")

    keyring_password = CONFIG_ENV.get("KEYRING_PASSWORD")
    node_rpc_url = CONFIG_ENV.get("NODE_RPC_URL", "http://127.0.0.1:26657")
    chain_id = CONFIG_ENV.get("CHAIN_ID")

    if not keyring_password:
        raise ValueError("KEYRING_PASSWORD not found in CONFIG_ENV")
    if not chain_id:
        raise ValueError("CHAIN_ID not found in CONFIG_ENV")

    cmd = [
        str(INFERENCED_BINARY.path),
        "tx", "inference", "grant-ml-ops-permissions",
        COLD_KEY_NAME,
        warm_key_address,
        "--from", COLD_KEY_NAME,
        "--keyring-backend", "file",
        "--home", str(INFERENCED_STATE_DIR),
        "--chain-id", chain_id,
        "--node", _normalize_node_rpc_url(node_rpc_url),
        "-y",
        "--gas", "auto",
        "--gas-adjustment", "1.5",
    ]
    gas_prices = (CONFIG_ENV.get("TX_GAS_PRICES") or "").strip()
    if gas_prices:
        cmd.extend(["--gas-prices", gas_prices])
    else:
        # Match testermint / pre-fee testnet joins: no --gas-prices → zero tx fee when chain min_gas_price=0.
        print("Grant tx: no TX_GAS_PRICES (zero-fee path for testnet)")

    print(f"Running command: {' '.join(cmd)}")
    
    max_retries = 5
    retry_delay = 15
    for attempt in range(max_retries):
        try:
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
            combined = (stdout or "") + "\n" + (stderr or "")

            if process.returncode == 0:
                print("ML operations permissions granted successfully!")
                if stdout:
                    print("Output:")
                    print(stdout)
                _verify_warm_account_after_grant(warm_key_address, node_rpc_url)
                return

            if "fee allowance already exists" in combined.lower():
                print("Feegrant already exists; proceeding.")
                _verify_warm_account_after_grant(warm_key_address, node_rpc_url)
                return

            if "insufficient funds" in combined.lower():
                cold_addr = _cold_address_from_keyring()
                hint = (
                    "Grant failed: insufficient GNK. If you passed --gas-prices manually, "
                    "retry without it (testnet min_gas_price=0), or fund the cold key.\n"
                )
                if grant_tx_requires_funding():
                    min_ngonka = int(CONFIG_ENV.get("GRANT_MIN_SPENDABLE_NGONKA", "20000000000"))
                    hint += _grant_funding_instructions(cold_addr, warm_key_address, min_ngonka)
                raise RuntimeError(hint)

            print(f"Grant permissions failed with return code: {process.returncode}")
            if stdout:
                print("Output:")
                print(stdout)
            if stderr:
                print("Error:")
                print(stderr)

            retryable = (
                "timed out waiting for transaction" in combined.lower()
                or "connection refused" in combined.lower()
                or "context deadline exceeded" in combined.lower()
                or "not found: key not found" in combined.lower()
                or "account" in combined.lower() and "not found" in combined.lower()
            )
            if retryable and attempt < max_retries - 1:
                print(f"Retrying grant in {retry_delay}s ({attempt + 1}/{max_retries})...")
                time.sleep(retry_delay)
                continue

            raise subprocess.CalledProcessError(process.returncode, cmd)

        except Exception as e:
            if attempt < max_retries - 1:
                print(f"Error granting ML operations permissions: {e}. Retrying in {retry_delay}s...")
                time.sleep(retry_delay)
                continue
            print(f"Error granting ML operations permissions: {e}")
            raise


def _rpc_height_from_status_payload(payload):
    """Return (ready, height, error_message) from a Comet /status JSON payload."""
    sync_info = payload.get("result", {}).get("sync_info", {})
    height_raw = sync_info.get("latest_block_height", "0")
    try:
        height = int(height_raw)
    except (TypeError, ValueError):
        height = 0
    if height >= 1:
        return True, height, ""
    return False, height, f"latest_block_height not ready yet: {height_raw}"


def _rpc_catching_up_from_status_payload(payload):
    """Return catching_up flag from a Comet /status JSON payload (defaults to True if missing)."""
    sync_info = payload.get("result", {}).get("sync_info", {})
    return bool(sync_info.get("catching_up", True))


def _normalize_node_rpc_url(node_rpc_url: str) -> str:
    """
    Normalize RPC URL for inferenced --node.

    - Proxied seed RPC (path contains /chain-rpc) must stay http(s) with trailing slash.
    - Direct local Comet (http://host:26657, no path) uses tcp:// for the CLI.
    """
    url = (node_rpc_url or "").strip()
    if not url:
        return url

    lower = url.lower()
    if "/chain-rpc" in lower:
        if not url.endswith("/"):
            url += "/"
        return url

    if lower.startswith("http://") or lower.startswith("https://"):
        rest = url.split("://", 1)[1]
        if "/" in rest:
            return url if url.endswith("/") else url + "/"
        return "tcp://" + rest.rstrip("/")

    return url


def participant_exists_on_seed(address: str, seed_api_url: str = None) -> bool:
    """Return True if the seed node's public API exposes this participant."""
    seed_api_url = (seed_api_url or CONFIG_ENV.get("SEED_API_URL", "")).strip().rstrip("/")
    if not seed_api_url:
        return False

    url = f"{seed_api_url}/v2/participants/{address}"
    try:
        with urllib.request.urlopen(url, timeout=10) as resp:
            if resp.status != 200:
                return False
            body = resp.read().decode("utf-8", errors="replace").strip()
            if not body:
                return False
            payload = json.loads(body)
            if isinstance(payload, dict):
                participant = payload.get("participant") or payload.get("Participant")
                if isinstance(participant, dict):
                    return bool(participant.get("address") or participant.get("Address"))
                return bool(payload.get("address") or payload.get("Address"))
            return True
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return False
        raise RuntimeError(f"participant query failed ({e.code}): {e.read().decode('utf-8', errors='replace')}")
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError) as e:
        raise RuntimeError(f"participant query failed for {address}: {e}")


def wait_for_participant_on_seed(
    address: str,
    seed_api_url: str = None,
    timeout_seconds: int = 120,
    poll_interval: int = 3,
):
    """Poll seed HTTP API until /v2/participants/{address} returns 200."""
    seed_api_url = (seed_api_url or CONFIG_ENV.get("SEED_API_URL", "")).strip().rstrip("/")
    if not seed_api_url:
        print("WARNING: SEED_API_URL not set, skipping participant availability wait")
        return

    url = f"{seed_api_url}/v2/participants/{address}"
    deadline = time.time() + timeout_seconds
    print(f"Waiting for participant on seed API: {url} (timeout {timeout_seconds}s)...")
    while time.time() < deadline:
        try:
            if participant_exists_on_seed(address, seed_api_url):
                print(f"Participant visible on seed: {address}")
                return
        except RuntimeError as e:
            print(f"  participant query error: {e}")
        time.sleep(poll_interval)

    print(
        f"WARNING: participant not visible on seed after {timeout_seconds}s "
        f"(registration may still have succeeded; check {url})"
    )


def auth_account_exists(address: str, node_rpc_url: str) -> bool:
    """Return True if the auth module has an account for this bech32 address."""
    inferenced_binary = INFERENCED_BINARY.path
    if not inferenced_binary.exists():
        raise FileNotFoundError(f"Inferenced binary not found at {inferenced_binary}")

    cmd = [
        str(inferenced_binary),
        "query",
        "auth",
        "account",
        address,
        "--node",
        _normalize_node_rpc_url(node_rpc_url),
        "-o",
        "json",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
    combined = (result.stdout or "") + (result.stderr or "")
    if result.returncode == 0:
        try:
            payload = json.loads(result.stdout)
        except json.JSONDecodeError:
            return False
        return bool(payload.get("account"))

    lowered = combined.lower()
    if "not found" in lowered or "does not exist" in lowered:
        return False
    raise RuntimeError(f"auth account query failed for {address}: {combined.strip()}")


def wait_for_auth_account(
    address: str,
    node_rpc_url: str,
    label: str,
    timeout_seconds: int = 300,
    poll_interval: int = 5,
    required: bool = True,
):
    """Poll until the address has an on-chain auth account."""
    deadline = time.time() + timeout_seconds
    print(f"Waiting for on-chain auth account ({label}): {address} (timeout {timeout_seconds}s)...")
    last_progress = 0.0
    while time.time() < deadline:
        try:
            if auth_account_exists(address, node_rpc_url):
                print(f"Auth account ready ({label}): {address}")
                return
        except subprocess.TimeoutExpired:
            print(f"  auth query timed out ({label}), retrying...")
        except RuntimeError as e:
            print(f"  auth query error: {e}")
        now = time.time()
        if now - last_progress >= 30:
            remaining = int(deadline - now)
            print(f"  still waiting for {label} ({remaining}s left)...")
            last_progress = now
        time.sleep(poll_interval)

    message = f"Auth account not found for {label} ({address}) after {timeout_seconds}s"
    if required:
        raise TimeoutError(message)
    print(f"WARNING: {message} — continuing anyway")


def _fetch_rpc_status_http(status_url: str, timeout_seconds: int = 5):
    """Fetch /status over HTTP. Returns (ok, height, error)."""
    try:
        with urllib.request.urlopen(status_url, timeout=timeout_seconds) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
        payload = json.loads(raw)
        return _rpc_height_from_status_payload(payload)
    except (
        urllib.error.URLError,
        urllib.error.HTTPError,
        TimeoutError,
        json.JSONDecodeError,
        ValueError,
        OSError,
    ) as e:
        return False, 0, str(e)


def _fetch_rpc_status_via_docker(container: str = "node", timeout_seconds: int = 10):
    """Fetch /status via wget inside the node container (join hosts omit host port mapping)."""
    cmd = [
        "docker", "exec", container,
        "wget", "-qO-", "http://127.0.0.1:26657/status",
    ]
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=timeout_seconds,
        )
    except subprocess.TimeoutExpired:
        return False, 0, f"docker exec {container} timed out after {timeout_seconds}s"
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        return False, 0, f"docker exec {container} failed: {detail}"
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as e:
        return False, 0, f"invalid JSON from docker exec: {e}"
    return _rpc_height_from_status_payload(payload)


def _host_rpc_unreachable(err: str) -> bool:
    lowered = err.lower()
    return (
        "connection refused" in lowered
        or "errno 111" in lowered
        or "failed to establish a new connection" in lowered
        or "name or service not known" in lowered
    )


def wait_for_rpc_ready(
    node_rpc_url: str,
    timeout_seconds: int = 180,
    poll_interval: int = 2,
    docker_container: str = "node",
):
    """Wait until Comet RPC is reachable and returns a valid status payload."""
    status_url = node_rpc_url.rstrip("/") + "/status"
    deadline = time.time() + timeout_seconds
    last_error = None
    use_docker = False

    print(f"Waiting for RPC readiness at {status_url} (timeout: {timeout_seconds}s)...")
    while time.time() < deadline:
        if not use_docker:
            ok, height, err = _fetch_rpc_status_http(status_url)
            if ok:
                print(f"RPC is ready at height {height} (host)")
                return
            last_error = err
            if _host_rpc_unreachable(err):
                use_docker = True
                print(
                    f"Host RPC unreachable ({err}); "
                    f"falling back to docker exec {docker_container} ..."
                )
        else:
            ok, height, err = _fetch_rpc_status_via_docker(container=docker_container)
            if ok:
                print(f"RPC is ready at height {height} (via {docker_container})")
                return
            last_error = err
        time.sleep(poll_interval)

    raise TimeoutError(f"RPC not ready after {timeout_seconds}s: {last_error}")


def wait_for_rpc_synced(
    node_rpc_url: str,
    timeout_seconds: int = 900,
    poll_interval: int = 10,
    docker_container: str = "node",
):
    """Wait until the local node reports catching_up=false."""
    status_url = node_rpc_url.rstrip("/") + "/status"
    deadline = time.time() + timeout_seconds
    last_error = None
    use_docker = False

    print(f"Waiting for node sync (catching_up=false) at {status_url} (timeout: {timeout_seconds}s)...")
    while time.time() < deadline:
        payload = None
        if not use_docker:
            try:
                with urllib.request.urlopen(status_url, timeout=5) as resp:
                    payload = json.loads(resp.read().decode("utf-8", errors="replace"))
            except (
                urllib.error.URLError,
                urllib.error.HTTPError,
                TimeoutError,
                json.JSONDecodeError,
                OSError,
            ) as e:
                last_error = str(e)
                if _host_rpc_unreachable(last_error):
                    use_docker = True
        else:
            ok, _, err = _fetch_rpc_status_via_docker(container=docker_container)
            if ok:
                cmd = [
                    "docker", "exec", docker_container,
                    "wget", "-qO-", "http://127.0.0.1:26657/status",
                ]
                result = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
                if result.returncode == 0:
                    try:
                        payload = json.loads(result.stdout)
                    except json.JSONDecodeError as e:
                        last_error = str(e)
                else:
                    last_error = (result.stderr or result.stdout or "").strip()
            else:
                last_error = err

        if payload is not None:
            catching_up = _rpc_catching_up_from_status_payload(payload)
            _, height, _ = _rpc_height_from_status_payload(payload)
            if not catching_up:
                print(f"Node is synced at height {height}")
                return
            last_error = f"still catching up at height {height}"
        time.sleep(poll_interval)

    raise TimeoutError(f"Node did not finish syncing after {timeout_seconds}s: {last_error}")


def print_join_deploy_summary(account_key: AccountKey, warm_key: AccountKey, chain_id: str):
    """Print addresses and post-deploy verification hints for join hosts."""
    public_url = CONFIG_ENV.get("PUBLIC_URL", "http://127.0.0.1:8000")
    admin_base = public_url.rstrip("/")
    if ":8000" in admin_base:
        admin_base = admin_base.replace(":8000", ":9200", 1)
    setup_report_url = f"{admin_base}/admin/v1/setup/report"

    print("\n=== JOIN HOST DEPLOYED ===")
    print(f"Chain ID:     {chain_id}")
    print(f"Cold key:     {account_key.address} ({COLD_KEY_NAME})")
    print(f"Warm key:     {warm_key.address} ({CONFIG_ENV.get('KEY_NAME', 'warm')})")
    print(f"Public URL:   {public_url}")
    print(f"Setup report: {setup_report_url}")
    print("Verify: curl -s", setup_report_url, "| jq .")
    print("==========================\n")


def republish_node_rpc_if_needed():
    """Apply RPC host port mapping on the node service (join compose omits it by default)."""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    rpc_override = working_dir / "docker-compose.rpc-override.yml"
    if not rpc_override.exists():
        return
    config_file = working_dir / "config.env"
    if not config_file.exists():
        return
    compose_files = get_compose_files_arg(include_mlnode=False)
    cmd = f"bash -c 'source {config_file} && docker compose {compose_files} up -d node'"
    print("Ensuring node publishes RPC on 127.0.0.1:26657...")
    subprocess.run(cmd, shell=True, cwd=working_dir, check=False)
    time.sleep(3)


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
        compose_files = standard_compose_files(include_mlnode=True)
    else:
        compose_files = ensure_compose_overrides(compose_files)
    
    if additional_args is None:
        additional_args = ["-d"]
    
    # Build docker compose command
    cmd_parts = ["docker", "compose"]
    
    # Add compose files
    for file in compose_files:
        cmd_parts.extend(["-f", file])
    
    # Add up command
    cmd_parts.append("up")
    
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
        print(f"Docker services startup failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, start_cmd)
    
    print("Docker services started successfully!")


def genesis_route(account_key: AccountKey, chain_id: str) -> AccountKey:
    print("\n=== GENESIS MODE: Initializing genesis node ===")
    run_genesis_initialization()
    add_genesis_account(account_key)

    consensus_key = extract_consensus_key()
    # Create/reuse warm key AFTER genesis init, because init may reset .inference.
    warm_key = get_or_create_warm_key()

    # Phase 3. GENTX and GENPARTICIPANT generation
    # Setup genesis.json file for local gentx generation
    setup_genesis_file()
    set_chain_id_in_genesis(chain_id)
    fund_distribution_module_account()
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

    fund_genesis_ibc_balance(account_key.address)
    
    set_chain_id_in_genesis(chain_id)

    copy_genesis_back_to_docker()
    copy_final_genesis_to_repo()
    return warm_key


def join_route(account_key: AccountKey, chain_id: str) -> AccountKey:
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

    start_docker_services(
        compose_files=["docker-compose.yml"],
        services=["tmkms", "node"],
        additional_args=["-d", "--no-deps"]
    )
    print("Waiting 15 seconds for node to start...")
    time.sleep(15)

    warm_key = get_or_create_warm_key()
    register_joining_participant(cold_address=account_key.address)
    return warm_key


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
  
  # Run in join mode (export host-specific env first: PUBLIC_URL, KEY_NAME, KEYRING_PASSWORD, SEED_*)
  python launch.py --mode join --branch testnet/main --chainid gonka-testnet-4
  
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

    # Create local 
    account_key = create_account_key()
    CONFIG_ENV["ACCOUNT_PUBKEY"] = account_key.pubkey
    create_config_env_file()
    
    # Clean up any containers that might have been started during setup
    docker_compose_down()  # Ensure clean state before starting new containers
    
    # Run the main processes
    pull_images()

    if is_genesis:
        warm_key = genesis_route(account_key, args.chainid)
    else:
        warm_key = join_route(account_key, args.chainid)

    # Phase 5. Start services
    if is_genesis:
        # Create runtime override for genesis nodes
        node_id = CONFIG_ENV.get("NODE_ID", "")
        if node_id:
            create_docker_compose_override(init_only=False, node_id=node_id)
            start_docker_services(
                compose_files=["docker-compose.yml", "docker-compose.mlnode.yml", "docker-compose.runtime-override.yml"]
            )
        else:
            raise ValueError("NODE_ID not found in CONFIG_ENV")
    else:
        start_docker_services(additional_args=["-d"])

    # Ensure feegrant/authz are in place for warm key tx submission in both modes.
    # On fresh genesis this is required because upgrade-time feegrant migration does not run.
    rpc_url_for_grant = CONFIG_ENV.get("NODE_RPC_URL", "http://127.0.0.1:26657")
    rpc_timeout = 900 if not is_genesis else 180
    if not is_genesis:
        republish_node_rpc_if_needed()
    wait_for_rpc_ready(rpc_url_for_grant, timeout_seconds=rpc_timeout)
    if not is_genesis:
        wait_for_rpc_synced(rpc_url_for_grant, timeout_seconds=rpc_timeout)
        wait_for_auth_account(account_key.address, rpc_url_for_grant, "cold (local RPC)")
        print(
            f"Granting ML ops to warm key {warm_key.address} "
            "(warm auth account is created by grant, not required beforehand)"
        )
        wait_for_cold_funding_before_grant(
            account_key.address, warm_key.address, rpc_url_for_grant
        )
    grant_key_permissions(warm_key.address)
    if is_genesis:
        setup_bounty_pool()
        setup_wrapped_token_registration()
    if not is_genesis:
        print_join_deploy_summary(account_key, warm_key, args.chainid)

if __name__ == "__main__":
    main()
