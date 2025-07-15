# Re-run this script when creating a new worker!

# Connet to the worker and run
# Use the K3S_TOKEN obtained from the k8s-control-plane:
#  sudo cat /var/lib/rancher/k3s/server/node-token
# K3S_URL points to the INTERNAL IP of the control plane.
# --node-ip should be the INTERNAL IP of this worker node.
# --flannel-iface should match your primary internal network interface (usually eth0 on GCP)
#    to check use: ip -4 addr show
K3S_TOKEN=""
CONTROL_PLANE_INTERNAL_IP="10.128.0.41"
WORKER_INTERNAL_IP="10.128.0.46"
curl -sfL https://get.k3s.io | K3S_URL=https://$CONTROL_PLANE_INTERNAL_IP:6443 \
    K3S_TOKEN="$K3S_TOKEN" \
    INSTALL_K3S_EXEC="agent --flannel-iface=ens4 \
    --node-ip $WORKER_INTERNAL_IP \
    --node-label nvidia.com/gpu=true" sh -

# Check k3s-agent service status
sudo systemctl status k3s-agent
