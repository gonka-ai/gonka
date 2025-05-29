# ./scripts/execute_voting_update.py
import os
import subprocess
import sys
import time
import json
import urllib.request
import urllib.error
from pathlib import Path
from dataclasses import dataclass

@dataclass
class Node:
    """
    Represents a node in the system with its name and associated pods.
    Equivalent to Kotlin's: data class Node(val name:String, val apiPod:String, nodePod: String)
    """
    name: str
    api_pod: str
    node_pod: str
    api_pod_namespace: str = ""
    node_pod_namespace: str = ""
    admin_port_local: int = 0  # Local port mapped to admin port (9200)
    public_port_local: int = 0  # Local port mapped to public port (9000)

    def setup_port_tunnels(self, base_port=10000):
        """
        Set up SSH tunnels for the admin port (9200) and public port (9000).
        Maps these ports to free local ports starting from base_port.

        Args:
            base_port (int): The starting port number for local port mapping.

        Returns:
            tuple: The mapped local ports (admin_port_local, public_port_local).
        """
        if not self.api_pod:
            raise ValueError("No api_pod specified for this Node")

        # Assign local ports
        self.admin_port_local = base_port
        self.public_port_local = base_port + 1

        # Set up tunnel for admin port (9200)
        admin_tunnel_command = ["kubectl", "port-forward"]

        # Add namespace if available
        if self.api_pod_namespace:
            admin_tunnel_command.extend(["-n", self.api_pod_namespace])

        # Add pod name and port mapping
        admin_tunnel_command.extend([
            self.api_pod,
            f"{self.admin_port_local}:9200"
        ])

        # Run the command in the background
        print(f"Setting up admin port tunnel for {self.name}: {self.admin_port_local} -> 9200")
        subprocess.Popen(admin_tunnel_command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        # Set up tunnel for public port (9000)
        public_tunnel_command = ["kubectl", "port-forward"]

        # Add namespace if available
        if self.api_pod_namespace:
            public_tunnel_command.extend(["-n", self.api_pod_namespace])

        # Add pod name and port mapping
        public_tunnel_command.extend([
            self.api_pod,
            f"{self.public_port_local}:9000"
        ])

        # Run the command in the background
        print(f"Setting up public port tunnel for {self.name}: {self.public_port_local} -> 9000")
        subprocess.Popen(public_tunnel_command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        # Wait for tunnels to establish
        time.sleep(2)

        return (self.admin_port_local, self.public_port_local)

    def _make_request(self, port, url_path, method="GET", payload=None):
        """
        Base method to make HTTP requests to a specific port.

        Args:
            port (int): The local port to send the request to.
            url_path (str): The URL path beyond localhost:port.
            method (str): The HTTP method (GET, POST, etc.).
            payload (dict, optional): The payload to send with the request.

        Returns:
            dict: The JSON response from the request.
        """
        # Construct the full URL
        url = f"http://localhost:{port}/{url_path.lstrip('/')}"

        # Prepare the request
        headers = {"Content-Type": "application/json"}
        data = None
        if payload:
            data = json.dumps(payload).encode('utf-8')

        # Create the request object
        req = urllib.request.Request(
            url=url,
            data=data,
            headers=headers,
            method=method
        )

        try:
            # Send the request and get the response
            with urllib.request.urlopen(req) as response:
                response_data = response.read().decode('utf-8')
                return json.loads(response_data) if response_data else {}
        except urllib.error.HTTPError as e:
            print(f"HTTP Error: {e.code} - {e.reason}")
            print(f"Response: {e.read().decode('utf-8')}")
            raise
        except urllib.error.URLError as e:
            print(f"URL Error: {e.reason}")
            raise
        except json.JSONDecodeError:
            print("Error decoding JSON response")
            raise

    def admin_request(self, url_path, method="GET", payload=None):
        """
        Make an HTTP request to the admin port (9200).

        Args:
            url_path (str): The URL path beyond localhost:port.
            method (str): The HTTP method (GET, POST, etc.).
            payload (dict, optional): The payload to send with the request.

        Returns:
            dict: The JSON response from the request.
        """
        if not self.admin_port_local:
            raise ValueError("Admin port not set up. Call setup_port_tunnels first.")

        return self._make_request(self.admin_port_local, url_path, method, payload)

    def public_request(self, url_path, method="GET", payload=None):
        """
        Make an HTTP request to the public port (9000).

        Args:
            url_path (str): The URL path beyond localhost:port.
            method (str): The HTTP method (GET, POST, etc.).
            payload (dict, optional): The payload to send with the request.

        Returns:
            dict: The JSON response from the request.
        """
        if not self.public_port_local:
            raise ValueError("Public port not set up. Call setup_port_tunnels first.")

        return self._make_request(self.public_port_local, url_path, method, payload)

    def exec_inferenced(self, args):
        """
        Execute the inferenced command on the node_pod using kubectl exec.

        Args:
            args (list): List of arguments to pass to the inferenced command.

        Returns:
            str: The stdout output from the command execution.
        """
        if not self.node_pod:
            raise ValueError("No node_pod specified for this Node")

        # Construct the kubectl exec command
        command = ["kubectl", "exec"]

        # Add namespace if available
        if self.node_pod_namespace:
            command.extend(["-n", self.node_pod_namespace])

        # Add pod name and command
        command.extend([
            self.node_pod, 
            "--",
            "inferenced"
        ])

        # Add the provided arguments
        command.extend(args)

        # Execute the command
        result = run_command(command)

        # Return the stdout as a string
        return result.stdout.strip() if result.stdout else ""

    def exec_inferenced_with_retry(self, args):
        """
        Execute the inferenced command on the node_pod using kubectl exec,
        but don't exit on error - instead, propagate the exception for retry logic to handle.

        Args:
            args (list): List of arguments to pass to the inferenced command.

        Returns:
            str: The stdout output from the command execution.

        Raises:
            subprocess.CalledProcessError: If the command execution fails.
            FileNotFoundError: If the command is not found.
        """
        if not self.node_pod:
            raise ValueError("No node_pod specified for this Node")

        # Construct the kubectl exec command
        command = ["kubectl", "exec"]

        # Add namespace if available
        if self.node_pod_namespace:
            command.extend(["-n", self.node_pod_namespace])

        # Add pod name and command
        command.extend([
            self.node_pod, 
            "--",
            "inferenced"
        ])

        # Add the provided arguments
        command.extend(args)

        # Execute the command directly with subprocess.run
        print(f"Executing: {' '.join(command)}")
        process = subprocess.run(command, check=True, capture_output=True, text=True)

        # Return the stdout as a string
        return process.stdout.strip() if process.stdout else ""

    def get_keys(self):
        """
        Get the list of keys from the node.

        Returns:
            list: A list of key objects.
        """
        output = self.exec_inferenced(["keys", "list", "--output", "json"])
        return json.loads(output)

    def generate_upgrade_proposal(self, upgrade_name, upgrade_height, upgrade_info, title=None, summary="", deposit="100000nicoin", from_address=None, chain_id="prod-sim"):
        """
        Generate an upgrade proposal transaction.

        Args:
            upgrade_name (str): The name of the upgrade.
            upgrade_height (int): The block height at which the upgrade should occur.
            upgrade_info (dict): Information about the upgrade, including binaries and versions.
            title (str, optional): The title of the proposal. Defaults to upgrade_name if not provided.
            summary (str, optional): A summary of the proposal.
            deposit (str, optional): The deposit amount for the proposal.
            from_address (str, optional): The address to use for the proposal. If not provided, uses the first key.
            chain_id (str, optional): The chain ID to use for the transaction.

        Returns:
            dict: The generated transaction.
        """
        if title is None:
            title = upgrade_name

        # If from_address is not provided, use the first key
        if from_address is None:
            keys = self.get_keys()
            if not keys:
                raise ValueError("No keys found on the node")
            from_address = keys[0]["address"]

        # Convert upgrade_info to a JSON string with minimal output
        upgrade_info_str = json.dumps(upgrade_info, separators=(',', ':'))
        print(f"Upgrade info: {upgrade_info_str}")
        # Build the command
        cmd = [
            "tx", "upgrade", "software-upgrade", upgrade_name,
            "--title", title,
            "--upgrade-height", str(upgrade_height),
            "--upgrade-info", upgrade_info_str,
            "--summary", summary,
            "--deposit", deposit,
            "--from", from_address,
            "--chain-id", chain_id,
            "--yes",
            "--broadcast-mode", "sync",
            "--output", "json",
            "--gas", "auto"
        ]

        # Execute the command
        output = self.exec_inferenced(cmd)

        # Parse the output as JSON
        try:
            return json.loads(output)
        except json.JSONDecodeError:
            print(f"Error parsing JSON output: {output}")
            raise

    def submit_transaction(self, transaction):
        """
        Submit a transaction to the API.

        Args:
            transaction (dict): The transaction to submit.

        Returns:
            dict: The response from the API.
        """
        return self.admin_request("admin/v1/tx/send", method="POST", payload=transaction)

    def wait_for_transaction(self, tx_response, max_retries=30, retry_interval=2):
        """
        Wait for a transaction to be posted and check its status.

        This method takes the response from sending a transaction (which contains a txhash),
        then uses the node_pod to query the transaction status until it's ready or max_retries is reached.

        The exec command itself will fail if the transaction is not ready, not just return unparseable output.
        This method handles both cases - exec failure and unparseable output.

        Args:
            tx_response (dict): The response from sending a transaction, containing a txhash.
            max_retries (int, optional): Maximum number of retry attempts. Defaults to 30.
            retry_interval (int, optional): Time in seconds between retries. Defaults to 2.

        Returns:
            dict: The transaction details once it's ready, or None if the transaction failed or timed out.
        """
        if not tx_response or 'txhash' not in tx_response:
            print("Error: Invalid transaction response, missing txhash")
            return None

        txhash = tx_response['txhash']
        print(f"Waiting for transaction {txhash} to be posted...")

        for attempt in range(max_retries):
            try:
                # Query the transaction status using the node_pod with the retry-friendly method
                cmd = ["query", "tx", "--type=hash", txhash, "--output", "json"]
                output = self.exec_inferenced_with_retry(cmd)

                # Try to parse the output as JSON
                try:
                    tx_details = json.loads(output)
                    # If we get here, the transaction has been posted successfully
                    print(f"Transaction {txhash} posted successfully (attempt {attempt+1})")
                    return tx_details
                except json.JSONDecodeError:
                    # If we can't parse the output as JSON, the transaction might not be posted yet
                    print(f"Transaction {txhash} not yet posted - invalid JSON response (attempt {attempt+1})")
                    print(f"Raw output: {output}")

            except subprocess.CalledProcessError as e:
                # Handle the case where the exec command itself fails (expected when TX is not ready)
                print(f"Transaction {txhash} not yet posted - exec failed (attempt {attempt+1})")
                if hasattr(e, 'stderr') and e.stderr:
                    print(f"Error output: {e.stderr}")

            except Exception as e:
                # Handle any other unexpected errors
                print(f"Unexpected error checking transaction status (attempt {attempt+1}): {str(e)}")

            # Wait before retrying
            if attempt < max_retries - 1:
                print(f"Waiting {retry_interval} seconds before retrying...")
                time.sleep(retry_interval)

        print(f"Transaction {txhash} not posted after {max_retries} attempts")
        return None

    def get_upgrade_json(self, upgrade_name, upgrade_height, node_binaries=None, api_binaries=None, node_version="",
                         title=None, summary="For testing", deposit="500000nicoin", from_address=None):
        """
        Submit an upgrade proposal.

        Args:
            upgrade_name (str): The name of the upgrade.
            upgrade_height (int): The block height at which the upgrade should occur.
            node_binaries (dict, optional): Dictionary mapping platform to node binary URLs.
            api_binaries (dict, optional): Dictionary mapping platform to API binary URLs.
            node_version (str, optional): The version of the node.
            title (str, optional): The title of the proposal. Defaults to upgrade_name if not provided.
            summary (str, optional): A summary of the proposal.
            deposit (str, optional): The deposit amount for the proposal.
            from_address (str, optional): The address to use for the proposal. If not provided, uses the first key.
            chain_id (str, optional): The chain ID to use for the transaction.

        Returns:
            dict: The response from the API.
        """
        # Set default values for binaries if not provided
        if node_binaries is None:
            node_binaries = {}
        if api_binaries is None:
            api_binaries = {}

        # Create the upgrade info
        upgrade_info = {
            "binaries": node_binaries,
            "api_binaries": api_binaries,
            "node_version": node_version
        }

        # Generate and submit the upgrade proposal directly
        return self.exec_inferenced([
            "tx", "upgrade", "software-upgrade", upgrade_name,
            "--title", title or upgrade_name,
            "--upgrade-height", str(upgrade_height),
            "--upgrade-info", json.dumps(upgrade_info, separators=(',', ':')),
            "--summary", summary,
            "--deposit", deposit,
            "--from", from_address or self.get_keys()[0]["address"],
            "--yes",
            "--broadcast-mode", "sync",
            "--output", "json",
            "--gas", "auto",
            "--generate-only"
        ])

def run_command(command, **kwargs):
    """Helper function to run a shell command and print its output."""
    print(f"Executing: {' '.join(command)}")
    try:
        process = subprocess.run(command, check=True, capture_output=True, text=True, **kwargs)
        if process.stdout:
            print("STDOUT:\n", process.stdout)
        if process.stderr:
            print("STDERR:\n", process.stderr)
        return process
    except subprocess.CalledProcessError as e:
        print(f"Error executing command: {' '.join(command)}")
        if e.stdout:
            print("STDOUT:\n", e.stdout)
        if e.stderr:
            print("STDERR:\n", e.stderr)
        sys.exit(1)
    except FileNotFoundError:
        print(f"Error: Command '{command[0]}' not found. Make sure it's installed and in PATH.")
        sys.exit(1)

def get_env_vars():
    """Read and validate environment variables."""
    env_vars = {
        "release_tag": os.environ.get("RELEASE_TAG"),
        "gce_project_id": os.environ.get("GCE_PROJECT_ID"),
        "gce_zone": os.environ.get("GCE_ZONE"),
        "k8s_control_plane_name": os.environ.get("K8S_CONTROL_PLANE_NAME"),
        "k8s_control_plane_user": os.environ.get("K8S_CONTROL_PLANE_USER")
    }

    # Print environment variables
    for key, value in env_vars.items():
        print(f"{key.replace('_', ' ').title()}: {value}")

    # Validate environment variables
    if not all(env_vars.values()):
        print("Error: One or more required environment variables are not set.")
        sys.exit(1)

    return env_vars

def setup_kubectl(env_vars):
    """Configure kubectl and return the kubeconfig path."""
    print("--- Configuring kubectl ---")
    kube_dir = Path.home() / ".kube"
    kube_dir.mkdir(parents=True, exist_ok=True)
    kubeconfig_path = kube_dir / "config"

    # Fetch kubeconfig
    gcloud_scp_command = [
        "gcloud", "compute", "scp",
        f"{env_vars['k8s_control_plane_user']}@{env_vars['k8s_control_plane_name']}:~/.kube/config",
        str(kubeconfig_path),
        "--zone", env_vars["gce_zone"],
        "--project", env_vars["gce_project_id"]
    ]
    run_command(gcloud_scp_command)

    # Set KUBECONFIG environment variable
    os.environ["KUBECONFIG"] = str(kubeconfig_path)

    return kubeconfig_path

def setup_ssh_tunnel(env_vars):
    """Set up SSH tunnel to the Kubernetes control plane."""
    print(f"Setting up SSH tunnel to {env_vars['k8s_control_plane_name']}...")
    gcloud_ssh_command = [
        "gcloud", "compute", "ssh",
        f"{env_vars['k8s_control_plane_user']}@{env_vars['k8s_control_plane_name']}",
        "--zone", env_vars["gce_zone"],
        "--project", env_vars["gce_project_id"],
        "--ssh-flag=-L 6443:127.0.0.1:6443 -N -f"  # -N: no remote commands, -f: forks to background
    ]
    run_command(gcloud_ssh_command)  # This will fork to background

    # Wait for SSH tunnel to establish
    print("Waiting for SSH tunnel to establish...")
    time.sleep(10)

def verify_kubectl_connection(kubeconfig_path):
    """Verify kubectl connection and print kubeconfig info."""
    print(f"KUBECONFIG set to: {os.environ['KUBECONFIG']}")
    if kubeconfig_path.exists():
        print("Kubeconfig content (first few lines):")
        with open(kubeconfig_path, "r") as f:
            for i, line in enumerate(f):
                if i >= 5:
                    break
                print(line.strip())
    else:
        print("Kubeconfig file does not exist at expected location.")

    # Verify kubectl connection
    print("Verifying kubectl connection (this might use the tunnel)...")
    run_command(["kubectl", "cluster-info"])
    run_command(["kubectl", "get", "nodes", "--request-timeout=60s"])

def print_versions():
    """Print kubectl and kustomize versions."""
    print("--- Versions ---")
    print("kubectl client version:")
    run_command(["kubectl", "version", "--client", "-o", "yaml"])

def get_worker_nodes_with_pods():
    """
    Find k8s worker nodes and their associated pods.
    Sets up port tunnels for each node.

    Returns:
        list[Node]: A list of Node objects, each containing the node name, associated pod names, and port mappings.
    """
    print("--- Getting Worker Nodes and Pods ---")

    # Get all nodes
    nodes_process = run_command(["kubectl", "get", "nodes", "-o", "name"])
    nodes_output = nodes_process.stdout.strip().split('\n')

    worker_nodes = []

    # Filter for nodes matching k8s-worker-\d pattern
    import re
    worker_pattern = re.compile(r'node/k8s-worker-\d+')

    for node_full_name in nodes_output:
        if worker_pattern.match(node_full_name):
            # Extract just the node name without the 'node/' prefix
            node_name = node_full_name.replace('node/', '')

            # Get pods running on this node with their namespaces
            pods_process = run_command([
                "kubectl", "get", "pods", "--all-namespaces",
                "--field-selector", f"spec.nodeName={node_name}",
                "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name", 
                "--no-headers"
            ])

            pods_with_ns = []
            if pods_process.stdout.strip():
                for pod_line in pods_process.stdout.strip().split('\n'):
                    parts = pod_line.split()
                    if len(parts) >= 2:
                        namespace = parts[0]
                        pod_name = parts[1]
                        pods_with_ns.append((namespace, pod_name))

            # Find the first pod starting with "api-" and "node-"
            api_pod = ""
            api_pod_namespace = ""
            node_pod = ""
            node_pod_namespace = ""

            for namespace, pod_name in pods_with_ns:
                if pod_name.startswith("api-") and not api_pod:
                    api_pod = pod_name
                    api_pod_namespace = namespace
                if pod_name.startswith("node-") and not node_pod:
                    node_pod = pod_name
                    node_pod_namespace = namespace

            # Create a Node object and add it to the list
            worker_nodes.append(Node(
                name=node_name, 
                api_pod=api_pod, 
                node_pod=node_pod,
                api_pod_namespace=api_pod_namespace,
                node_pod_namespace=node_pod_namespace
            ))

    # Set up port tunnels for each node
    base_port = 10000
    for i, node in enumerate(worker_nodes):
        # Use a different base port for each node to avoid conflicts
        node_base_port = base_port + (i * 2)  # Increment by 2 for each node
        try:
            admin_port, public_port = node.setup_port_tunnels(base_port=node_base_port)
            print(f"Node {node.name} port mappings:")
            print(f"  Admin port: localhost:{admin_port} -> {node.api_pod}:9200")
            print(f"  Public port: localhost:{public_port} -> {node.api_pod}:9000")
        except Exception as e:
            print(f"Error setting up port tunnels for node {node.name}: {e}")

    return worker_nodes

def main():
    print("--- Starting Voting Update Script (Python) ---")

    # Initialize environment and setup
    env_vars = get_env_vars()
    kubeconfig_path = setup_kubectl(env_vars)
    setup_ssh_tunnel(env_vars)
    verify_kubectl_connection(kubeconfig_path)
    print_versions()

    # Actual voting update logic
    print("--- Performing Voting Update Actions ---")
    print(f"Using Release Tag: {env_vars['release_tag']}")
    return
    # Get worker nodes and their pods
    worker_nodes = get_worker_nodes_with_pods()


    # Print the results
    print(f"Found {len(worker_nodes)} worker nodes:")
    for node in worker_nodes:
        print(f"Node: {node.name}")
        print(f"  API Pod: {node.api_pod}")
        print(f"  Node Pod: {node.node_pod}")
        print(f"  Admin port: localhost:{node.admin_port_local} -> {node.api_pod}:9200")
        print(f"  Public port: localhost:{node.public_port_local} -> {node.api_pod}:9000")
    time.sleep(10)

    first_node = worker_nodes[0]
    print(first_node.exec_inferenced(["version"]))

    # Example of using the submit_upgrade function
    if env_vars.get('release_tag') and len(worker_nodes) > 0:
        print("--- Submitting Upgrade Proposal ---")

        # Example upgrade parameters
        upgrade_name = f"v{env_vars['release_tag']}"
        upgrade_height = 60  # Example height, should be determined based on current chain height

        # Example binary URLs
        node_binaries = {
            "linux/amd64": f"https://github.com/product-science/race-releases/releases/download/release%2Fv0.1.5/inferenced-amd64.zip?checksum=sha256:cc438e023be7bef75f98a34aaaf184d73196ecbaa4c6c59c8acbbb79d69d1a0b"
        }
        api_binaries = {
            "linux/amd64": f"https://github.com/product-science/race-releases/releases/download/release%2Fv0.1.5/decentralized-api-amd64.zip?checksum=sha256:5611b9bbc6416f30188451f7f49c9d2d93dd497b50c3c1dd29b44e65f40f8841"
        }
        print(first_node.admin_request("admin/v1/nodes"))

        # Submit the upgrade proposal
        try:
            response = first_node.get_upgrade_json(
                upgrade_name=upgrade_name,
                upgrade_height=upgrade_height,
                node_binaries=node_binaries,
                api_binaries=api_binaries,
                node_version="",
                title=upgrade_name,
                summary=f"Upgrade to {upgrade_name}",
                deposit="500000nicoin",
                from_address="genesis"
            )
        except Exception as e:
            print(f"Error submitting upgrade proposal: {e}")

        response_obj = json.loads(response)
        print(f"Upgrade proposal prepared, ready to send: {json.dumps(response_obj, indent=2)}")

        # Send the transaction
        tx_response = first_node.admin_request("admin/v1/tx/send", method="POST", payload=response_obj)
        print(f"Transaction sent, response: {json.dumps(tx_response, indent=2)}")

        # Wait for the transaction to be posted and get its details
        tx_details = first_node.wait_for_transaction(tx_response)
        if tx_details:
            print(f"Transaction details: {json.dumps(tx_details, indent=2)}")
        else:
            print("Failed to get transaction details")

    # print("--- Voting Update Script (Python) Finished ---")

if __name__ == "__main__":
    main()
