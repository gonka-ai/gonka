# Genesis Node K3s Deployment

This directory contains Kubernetes manifests for deploying the Genesis Node on a k3s cluster.

## Prerequisites

- A running k3s cluster with at least one worker node that has GPU support (`k8s-worker-1`)
- `kubectl` configured to access your cluster
- SSH access from your management machine (or `k8s-control-plane`) to `k8s-worker-1` for state cleaning.
- `stern` (optional, for improved log viewing)

## Deployment

1. **Configure kubectl** either by:
   - Copying `/etc/rancher/k3s/k3s.yaml` from `k8s-control-plane` to `~/.kube/config` locally, or
   - Setting up an SSH tunnel (see Appendix)

2. **Set up GitHub Container Registry authentication**:
   ```bash
   kubectl create secret docker-registry ghcr-credentials \
     --docker-server=ghcr.io \
     --docker-username=YOUR_GITHUB_USERNAME \
     --docker-password=YOUR_GITHUB_TOKEN
   ```
   Replace `YOUR_GITHUB_USERNAME` with your GitHub username and `YOUR_GITHUB_TOKEN` with a Personal Access Token that has `read:packages` permission. (If the secret already exists, this command will fail, which is fine.)

3. **Deploy the Genesis Node**:
   ```bash
   kubectl apply -f .
   ```

4. **Verify deployment**:
   ```bash
   kubectl get pods -w
   ```
   Wait until all pods (`node-0`, `api-*`, `tmkms-*`, `inference-*`) show `Running` status.

## Managing the Deployment

### View Logs

**Using kubectl** (individual components):
```bash
kubectl logs -f node-0                 # Node logs
kubectl logs -f $(kubectl get pod -l app=api -o name)        # API logs
kubectl logs -f $(kubectl get pod -l app=tmkms -o name)      # TMKMS logs
kubectl logs -f $(kubectl get pod -l app=inference -o name)  # Inference logs
```

**Using stern** (all components):
```bash
stern 'node|api|tmkms|inference' --exclude-container=POD
```

### Restart Components

```bash
kubectl rollout restart statefulset/node       # Restart node
kubectl rollout restart deployment/api         # Restart API
kubectl rollout restart deployment/tmkms       # Restart TMKMS
kubectl rollout restart deployment/inference   # Restart inference
```

### Update Configuration

1. Edit the ConfigMap:
   ```bash
   kubectl edit configmap config
   ```

2. Restart affected components:
   ```bash
   kubectl rollout restart statefulset/node deployment/api
   ```

### Stop Deployment (Delete Kubernetes Resources)

This stops the application but leaves data on the `hostPath` volumes intact.

```bash
kubectl delete -f .
```

### Clean Restart (Delete Kubernetes Resources and Clear State)

This performs a full reset, deleting Kubernetes resources and clearing persisted data from `hostPath` volumes on `k8s-worker-1`, `k8s-worker-2`, and `k8s-worker-3`.

**1. Delete Existing Kubernetes Application Resources:**
   Run this from where your `kubectl` is configured (e.g., your local machine or `k8s-control-plane`):
   ```bash
   kubectl delete -f . --ignore-not-found=true # Deletes app resources, ignores if clear-state-job.yaml is not found or vice-versa
   kubectl delete job clear-worker-state-job --ignore-not-found=true # Ensure previous job is cleaned up
   ```
   Wait for all resources to be terminated.

**2. Clear HostPath Volume Data using a Kubernetes Job:**
   Apply the `clear-state-job.yaml` manifest. This job will run pods on `k8s-worker-1`, `k8s-worker-2`, and `k8s-worker-3` to delete the contents of the specified host directories.
   ```bash
   kubectl apply -f clear-state-job.yaml
   ```

**3. Monitor the State Clearing Job:**
   Check the status of the job:
   ```bash
   kubectl get job clear-worker-state-job -w
   ```
   Wait for the job to show `COMPLETIONS` as `3/3`.

   View logs from the job's pods to confirm successful clearance on each node:
   ```bash
   kubectl logs -l app=clear-worker-state --tail=-1 # Shows all logs from all pods of the job
   ```

**4. Delete the State Clearing Job (Important):**
   Once the job is complete, delete it to avoid re-running it accidentally and to clean up the completed pods.
   ```bash
   kubectl delete job clear-worker-state-job
   ```

**5. Re-deploy Application:**
   Follow steps 2-4 from the main [Deployment](#deployment) section (create GHCR secret if needed, then `kubectl apply -f .` excluding `clear-state-job.yaml` if you re-applied everything from the directory).

   A safer re-deploy command after cleanup:
   ```bash
   kubectl apply -f api-deployment.yaml -f api-service.yaml -f config.yaml -f genesis-overrides-configmap.yaml -f inference-deployment.yaml -f inference-service.yaml -f node-config-configmap.yaml -f node-service.yaml -f node-statefulset.yaml -f tmkms-deployment.yaml
   ```

   *Note: The `initContainer` in `tmkms-deployment.yaml` should handle permissions for its directory. If permission issues arise for `/srv/dai/inference` (used by `node` and `api`), consider adding similar `initContainers` to their respective manifests.*

## Appendix: SSH Tunnel Setup

If accessing the cluster remotely from your local machine, set up an SSH tunnel:

```bash
# Start tunnel
gcloud compute ssh k8s-control-plane \
    --project=YOUR_GCP_PROJECT_ID \
    --zone=YOUR_GCE_INSTANCE_ZONE \
    -- -L 6443:127.0.0.1:6443 -N -f

# Check tunnel status
pgrep -f 'ssh.*-L 6443:127.0.0.1:6443' > /dev/null && echo "Tunnel ACTIVE" || echo "Tunnel NOT ACTIVE"

# Kill tunnel
pkill -f 'ssh.*-L 6443:127.0.0.1:6443'
```

Update your kubeconfig's server field to: `https://127.0.0.1:6443`
