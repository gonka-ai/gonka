# ./scripts/execute_voting_update.py
import os
import subprocess
import sys
import time
from pathlib import Path

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

def main():
    print("--- Starting Voting Update Script (Python) ---")

    # Read environment variables
    release_tag = os.environ.get("RELEASE_TAG")
    gce_project_id = os.environ.get("GCE_PROJECT_ID")
    gce_zone = os.environ.get("GCE_ZONE")
    k8s_control_plane_name = os.environ.get("K8S_CONTROL_PLANE_NAME")
    k8s_control_plane_user = os.environ.get("K8S_CONTROL_PLANE_USER")

    print(f"Release Tag: {release_tag}")
    print(f"GCE Project ID: {gce_project_id}")
    print(f"GCE Zone: {gce_zone}")
    print(f"K8s Control Plane Name: {k8s_control_plane_name}")
    print(f"K8s Control Plane User: {k8s_control_plane_user}")

    if not all([release_tag, gce_project_id, gce_zone, k8s_control_plane_name, k8s_control_plane_user]):
        print("Error: One or more required environment variables are not set.")
        sys.exit(1)

    # 1. Configure kubectl
    print("--- Configuring kubectl ---")
    kube_dir = Path.home() / ".kube"
    kube_dir.mkdir(parents=True, exist_ok=True)
    kubeconfig_path = kube_dir / "config"

    print(f"Fetching kubeconfig from {k8s_control_plane_name} in zone {gce_zone}...")
    gcloud_scp_command = [
        "gcloud", "compute", "scp",
        f"{k8s_control_plane_user}@{k8s_control_plane_name}:~/.kube/config",
        str(kubeconfig_path),
        "--zone", gce_zone,
        "--project", gce_project_id
    ]
    run_command(gcloud_scp_command)

    # Set KUBECONFIG environment variable for subsequent kubectl commands within this script
    os.environ["KUBECONFIG"] = str(kubeconfig_path)

    print(f"Setting up SSH tunnel to {k8s_control_plane_name}...")
    # Note: For the SSH tunnel, running it as a background process from Python
    # can be a bit tricky to manage. gcloud compute ssh itself handles daemonizing with -f.
    # Ensure your kubeconfig is set to use 127.0.0.1:6443 if the API server isn't public.
    gcloud_ssh_command = [
        "gcloud", "compute", "ssh",
        f"{k8s_control_plane_user}@{k8s_control_plane_name}",
        "--zone", gce_zone,
        "--project", gce_project_id,
        "--ssh-flag=-L 6443:127.0.0.1:6443 -N -f" # -N: no remote commands, -f: forks to background
    ]
    run_command(gcloud_ssh_command) # This will fork to background

    print("Waiting for SSH tunnel to establish...")
    time.sleep(10) # Give the tunnel some time

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

    print("Verifying kubectl connection (this might use the tunnel)...")
    run_command(["kubectl", "cluster-info"])
    run_command(["kubectl", "get", "nodes", "--request-timeout=60s"])

    # 2. Print kubectl and kustomize versions
    print("--- Versions ---")
    print("kubectl client version:")
    run_command(["kubectl", "version", "--client", "-o", "yaml"])
    # If you use kustomize, you could add a version check here too

    # 3. Your actual voting update logic will go here
    print("--- Performing Voting Update Actions (Placeholder) ---")
    print(f"Using Release Tag: {release_tag}")
    # Example: run_command(["kubectl", "apply", "-f", "your-voting-config.yaml", "--namespace", "your-namespace"])
    # Example: run_command(["kubectl", "set", "image", "deployment/my-app", f"my-container=your-image:{release_tag}"])
    # ... add your kubectl commands or further script calls here ...

    print("--- Voting Update Script (Python) Finished ---")

if __name__ == "__main__":
    main()