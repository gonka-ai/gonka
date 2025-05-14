# No need to run this script when creating a new worker

# should be run on k8s-control-plane machine
# We use dev user:
# gcloud compute ssh dev@k8s-control-plane

# The --flannel-iface should match your primary internal network interface (usually eth0 on GCP),
#    to check use: ip -4 addr show
# The --node-ip should be the INTERNAL IP of this control plane node.
# We disable servicelb and traefik for a minimal setup. You can add them later.
CONTROL_PLANE_INTERNAL_IP="10.128.0.41"
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--flannel-iface=ens4 \
    --node-ip $CONTROL_PLANE_INTERNAL_IP \
    --disable servicelb \
    --disable traefik \
    --write-kubeconfig-mode 644" sh -

# Check k3s service status
sudo systemctl status k3s

# Set up kubectl access (for the root user on the control plane)
mkdir -p $HOME/.kube
sudo cp /etc/rancher/k3s/k3s.yaml $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config
export KUBECONFIG=$HOME/.kube/config

# To access from your local machine:
# 0. Install kubectl
#   brew install kubectl
#   # Verify installation
#   kubectl version --client
# 1. Copy the content of /etc/rancher/k3s/k3s.yaml from the control plane.
#    if not exist: mkdir -p ~/.kube
#    gcloud compute scp dev@k8s-control-plane:/etc/rancher/k3s/k3s.yaml ~/.kube/k3s-config
# 2. Tunnel to the machine:
#    gcloud compute ssh dev@k8s-control-plane -- -L 6443:localhost:6443
# 3. Then use on you local machine: export KUBECONFIG=~/.kube/k3s-config ; kubectl get nodes
