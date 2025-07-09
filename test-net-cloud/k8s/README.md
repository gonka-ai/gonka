Access services via NodePort (k3s has ServiceLB disabled):
- Proxy (main entry point): Use any worker node's external IP on port 30000
- Explorer dashboard: Available at `http://<worker-external-ip>:30000/` (root path)
- API endpoints: Available at `http://<worker-external-ip>:30000/api/v1/` or `http://<worker-external-ip>:30000/v1/`
- Chain RPC: Available at `http://<worker-external-ip>:30000/chain-rpc/`
- Chain REST API: Available at `http://<worker-external-ip>:30000/chain-api/`

Run genesis node

```bash
kubectl create namespace genesis # if not already created
kubectl apply -k k8s/genesis -n genesis
```

Get worker external IPs:
```bash
kubectl get nodes -o wide
# Use any worker node's EXTERNAL-IP with port 30000
```

Stop genesis node
```bash
kubectl delete all --all -n genesis
```

Run join-worker-2 (includes proxy and explorer)

```bash
kubectl create namespace join-k8s-worker-2 # if not already created
kubectl apply -k k8s/overlays/join-k8s-worker-2 -n join-k8s-worker-2
```

Access worker-2 services:
```bash
kubectl get nodes -o wide
# Use worker-2's EXTERNAL-IP with port 30000: http://<worker-2-external-ip>:30000/
```

Stop join-worker-2 and delete pvc
```bash
kubectl delete all --all -n join-k8s-worker-2

# To delete pvc
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-2
```

Run join-worker-3 (includes proxy and explorer)

```bash
kubectl create namespace join-k8s-worker-3 # if not already created
kubectl apply -k k8s/overlays/join-k8s-worker-3 -n join-k8s-worker-3
```

Access worker-3 services:
```bash
kubectl get nodes -o wide
# Use worker-3's EXTERNAL-IP with port 30000: http://<worker-3-external-ip>:30000/
```

Stop join-worker-3
```bash
kubectl delete all --all -n join-k8s-worker-3

# To delete pvc
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-3
```

```bash
# Clean state
gcloud compute ssh k8s-worker-1 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-2 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-3 --zone us-central1-a --command "sudo rm -rf /srv/dai"
```

Stop all
```bash
# Delete all resources
kubectl delete all --all -n genesis
kubectl delete all --all -n join-k8s-worker-2
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-2
kubectl delete all --all -n join-k8s-worker-3
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-3
# Delete data
gcloud compute ssh k8s-worker-1 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-2 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-3 --zone us-central1-a --command "sudo rm -rf /srv/dai"
```
