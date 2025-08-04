#!/bin/bash

# This script provides a set of commands to manually reproduce the consensus failure
# scenario on your Kubernetes cluster. It's intended to be run step-by-step,
# with manual verification at each stage.
#
# Pre-requisites:
# 1. You have a running Kubernetes cluster.
# 2. `kubectl` is configured to connect to your cluster.
# 3. You have an SSH tunnel or direct access to the Kubernetes API server,
#    as set up in the 'deploy-test-net-cloud.yml' GitHub workflow.

# --- Step 1: Initial Deployment ---

echo "### Step 1: Deploy the initial 3-node cluster ###"
echo "Run the 'Deploy Test Net Cloud' GitHub Actions workflow to deploy the genesis node and two joiner nodes."
echo "Wait for the workflow to complete successfully."
echo "Once done, verify that all pods are running in their respective namespaces."
echo ""
echo "kubectl get pods -n genesis"
echo "kubectl get pods -n join-k8s-worker-2"
echo "kubectl get pods -n join-k8s-worker-3"
echo ""
echo "Press enter to continue to the next step..."
read

# --- Step 2: Wait for a PoC cycle to complete ---

echo "### Step 2: Wait for a Proof-of-Compute (PoC) cycle to complete ###"
echo "We need to ensure the network is stable and has completed at least one full epoch."
echo "You can monitor the logs of the genesis node for epoch stage transitions."
echo "Look for messages indicating stages like 'poc-validation', 'set-new-validators', and 'claim-rewards'."
echo "Find the genesis pod name first:"
echo ""
echo "kubectl get pods -n genesis"
echo ""
echo "# Replace <genesis-pod-name> with the actual pod name from the command above."
echo "# You are looking for a log line like: 'INF finished stage name=claim-rewards'"
echo "kubectl logs -f <genesis-pod-name> -n genesis -c node | grep 'stage name'"
echo ""
echo "Once you see a 'claim-rewards' stage complete, a PoC cycle is over."
echo "Press enter to continue to the next step..."
read

# --- Step 3: Simulate Node Disconnection ---

# We will disconnect the node corresponding to the 'join-k8s-worker-3' namespace.
TARGET_NAMESPACE="join-k8s-worker-3"
K8S_WORKER_NAME="k8s-worker-3"
GCE_ZONE="us-central1-a"
GCE_PROJECT_ID="decentralized-ai"

# Step 3: Disconnect node in namespace '$TARGET_NAMESPACE' ###"
# This is done by deleting all its Kubernetes resources, including the persistent volume claim."

# Delete all resources (Deployments, Services, Pods, etc.) in the namespace
kubectl delete all --all -n $TARGET_NAMESPACE
kubectl delete pvc tmkms-data-pvc -n $TARGET_NAMESPACE --ignore-not-found=true
gcloud compute ssh ${K8S_WORKER_NAME} --zone ${GCE_ZONE} --project ${GCE_PROJECT_ID} --command "sudo rm -rf /srv/dai"

# Wait a few seconds for resources to be terminated
sleep 15
# Verify that the resources are gone:"
kubectl get all -n $TARGET_NAMESPACE

# --- Step 4: Simulate a New Node Joining ---

echo "### Step 4: Re-deploy the resources for '$TARGET_NAMESPACE' to simulate a new node joining ###"
echo "This is done by re-applying the kustomization for that node."
echo "This simulates a fresh node joining the network as a new participant."
echo ""
echo "# Note: We assume you are running this from the root of the 'gonka' repository."
# WARNING
# Change version in image-versions/kustomization.yaml
# Look for recent releases here if you deployed via GitHub Actions workflow:
# https://github.com/product-science/pivot-deploy/pkgs/container/inferenced
kubectl apply -k test-net-cloud/k8s/overlays/join-k8s-worker-3 -n $TARGET_NAMESPACE
echo ""
echo "Wait for the new pod to be created and start running."
echo "You can monitor its status with:"
echo "kubectl get pods -n $TARGET_NAMESPACE -w"
echo ""
echo "Press enter to continue to the final verification step..."
read

# --- Step 5: Verify if Consensus has Failed ---

echo "### Step 5: Verify if the chain has halted ###"
echo "If the bug is reproduced, the chain will stop producing new blocks due to a consensus failure."
echo "Monitor the logs of one of the original, running nodes (e.g., genesis)."
echo "If you stop seeing 'committed new block' messages for more than 30-60 seconds, the chain has likely halted."
echo ""

echo "# Get the pod name for the genesis node"
echo "kubectl get pods -n genesis"
echo ""
echo "# Replace <genesis-pod-name> and tail the logs. Look for 'committed new block'."
echo "kubectl logs -f <genesis-pod-name> -n genesis -c node"
echo ""
echo "If the logs are silent or you see 'CONSENSUS FAILURE' errors, you have successfully reproduced the issue." 