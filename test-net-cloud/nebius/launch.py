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
    url="https://github.com/product-science/race-releases/releases/download/release%2Fv0.2.8-poc3/inferenced-linux-amd64.zip",
    checksum="5ab63bfb05ae2ccc85627beb0ff3ced6b0bddf97ba864eae359bdcb0fa730e3e",
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
        
        # Check if env-override file exists
        env_override_file = DEPLOY_DIR / "docker-compose.env-override.yml"
        compose_files = ["-f", "docker-compose.yml", "-f", "docker-compose.mlnode.yml"]
        if env_override_file.exists():
            compose_files.extend(["-f", "docker-compose.env-override.yml"])
        
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
        if "gonka-account-key" in result.stdout:
            print("Account key 'gonka-account-key' already exists")
            return
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
    """Create docker-compose override file to inject IS_TEST_NET into all containers"""
    working_dir = GONKA_REPO_DIR / "deploy/join"
    override_file = working_dir / "docker-compose.env-override.yml"
    
    is_test_net = CONFIG_ENV.get("IS_TEST_NET", "true")
    
    override_content = f"""# Auto-generated environment override - do not commit
services:
  tmkms:
    environment:
      - IS_TEST_NET={is_test_net}
  node:
    environment:
      - IS_TEST_NET={is_test_net}
  api:
    environment:
      - IS_TEST_NET={is_test_net}
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
    files.append("docker-compose.env-override.yml")
    
    args = []
    for f in files:
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
    
    if init_only:
        override_file = working_dir / "docker-compose.genesis-override.yml"
        override_content = """services:
  node:
    ports:
      - "26657:26657"
    environment:
      - INIT_ONLY=true
      - IS_GENESIS=true
      - COIN_DENOM=ngonka
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
    
    print(f"Creating warm key for service: {service}")
    
    # Create the key
    compose_files = get_compose_files_arg(include_mlnode=True)
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


def generate_gentx(account_key: AccountKey, consensus_key: str, node_id: str, warm_key_address: str):
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
        "--chain-id", "gonka-mainnet",
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


def register_joining_participant(service="api"):
    """
    Register this node as a new participant in the existing network using Docker compose
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
    
    result = subprocess.run(
        register_cmd,
        shell=True,
        cwd=working_dir,
        capture_output=True,
        text=True
    )
    
    print("Participant registration completed!")
    print("Output:")
    print("=" * 50)
    if result.stdout:
        print(result.stdout)
    if result.stderr:
        print("Errors/Warnings:")
        print(result.stderr)
    print("=" * 50)
    
    if result.returncode != 0:
        print(f"Participant registration failed with return code: {result.returncode}")
        raise subprocess.CalledProcessError(result.returncode, register_cmd)
    
    print("Participant registration completed successfully!")


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


def genesis_route(account_key: AccountKey):
    print("\n=== GENESIS MODE: Initializing genesis node ===")
    run_genesis_initialization()
    add_genesis_account(account_key)

    consensus_key = extract_consensus_key()
    warm_key = get_or_create_warm_key()

    # Phase 3. GENTX and GENPARTICIPANT generation
    # Setup genesis.json file for local gentx generation
    setup_genesis_file()
    fund_distribution_module_account()
    # Generate gentx transaction
    node_id = CONFIG_ENV.get("NODE_ID", "")
    if not node_id:
        raise ValueError("NODE_ID not found in CONFIG_ENV")
    generate_gentx(account_key, consensus_key, node_id, warm_key.address)

    # Phase 4. Genesis finalization
    collect_genesis_transactions()
    patch_genesis_participants()

    # Apply genesis overrides (includes denom_metadata and other configurations)
    genesis_overrides_path = GONKA_REPO_DIR / "test-net-cloud/nebius/genesis-overrides.json"
    apply_genesis_overrides(genesis_overrides_path)

    copy_genesis_back_to_docker()
    copy_final_genesis_to_repo()


def join_route(account_key: AccountKey):
    print("\n=== JOIN MODE: Joining existing network ===")
    start_docker_services(
        compose_files=["docker-compose.yml"],
        services=["tmkms", "node"],
        additional_args=["-d", "--no-deps"]
    )
    print("Waiting 15 seconds for node to start...")
    time.sleep(15)
    
    # Get warm key for ML operations
    warm_key = get_or_create_warm_key()
    
    register_joining_participant()
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
    
    return parser.parse_args()


def main():
    # Parse command-line arguments
    args = parse_arguments()
    
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
        genesis_route(account_key)
    else:
        join_route(account_key)

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
        start_docker_services(
            compose_files=["docker-compose.yml", "docker-compose.mlnode.yml"],
            additional_args=["-d"]
        )

if __name__ == "__main__":
    main()
