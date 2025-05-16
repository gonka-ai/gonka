# No need to run this script when creating a new worker!

# Ensure your VMs have these tags in GCP:
# k8s-control-plane VM: network tag `k8s-control-plane`
# k8s-worker-1 VM: network tag `k8s-worker`

# --- Run these gcloud commands from your local machine or Cloud Shell ---

# Allow K8s API server access (TCP 6443) from workers to control plane
gcloud compute firewall-rules create k8s-api-server-ingress \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=tcp:6443 \
    --source-tags=k8s-worker \
    --target-tags=k8s-control-plane \
    --description="Allow worker nodes to access the K8s API server on the control plane"

# Allow Kubelet API access (TCP 10250) from control plane to workers
gcloud compute firewall-rules create k8s-kubelet-api-ingress \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=tcp:10250 \
    --source-tags=k8s-control-plane \
    --target-tags=k8s-worker \
    --description="Allow control plane to access Kubelet API on worker nodes (for metrics, logs, exec)"

# Allow Flannel VXLAN (UDP 8472) for pod networking between all K8s nodes
gcloud compute firewall-rules create k8s-flannel-vxlan-internode \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=udp:8472 \
    --source-tags=k8s-control-plane,k8s-worker \
    --target-tags=k8s-control-plane,k8s-worker \
    --description="Allow Flannel VXLAN traffic for pod networking between all K8s nodes"

# (Optional but Recommended) Allow SSH from your IP
# If your existing 'ssh-access' tag and rule are sufficient, you might not need this.
# But if you want a rule specifically for these K8s nodes using their tags:
# gcloud compute firewall-rules create k8s-ssh-ingress-from-mylocalsystem \
#     --project=decentralized-ai \
#     --direction=INGRESS \
#     --priority=1000 \
#     --network=default \
#     --action=ALLOW \
#     --rules=tcp:22 \
#     --source-ranges=YOUR_LOCAL_MACHINE_IP/32 \
#     --target-tags=k8s-control-plane,k8s-worker \
#     --description="Allow SSH access to K8s nodes from my local system"

# Expose NodePort 30000-30004 on every VM tagged “k8s-worker”
gcloud compute firewall-rules create k8s-nodeport-ingress \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=tcp:30000-30002 \
    --source-ranges=0.0.0.0/0 \
    --target-tags=k8s-worker \
    --description="Expose Kubernetes NodePort range 30000-30004 on worker nodes"

# If you want to tweak this rule later
gcloud compute firewall-rules update k8s-nodeport-ingress \
    --rules=tcp:30000-30029

# The below rule is not really needed, since we are using NodePort for now
gcloud compute firewall-rules create gonka-ingress \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=tcp:9000,tcp:26656,tcp:26657 \
    --source-ranges=0.0.0.0/0 \
    --target-tags=k8s-worker \
    --description="Expose ports necessary for the work of the gonka chain"

gcloud compute firewall-rules create headlamp-ingress \
    --project=decentralized-ai \
    --direction=INGRESS \
    --priority=1000 \
    --network=default \
    --action=ALLOW \
    --rules=tcp:32080 \
    --source-ranges=0.0.0.0/0 \
    --target-tags=k8s-worker \
    --description="Expose port 32080 to serve headlamp"
