Run genesis node

```bash
kubectl create namespace genesis # if not already created
kubectl apply -k k3s/genesis -n genesis
```

Stop genesis node
```bash
kubectl delete all --all -n genesis
```

Run join-worker-2

```bash
kubectl create namespace join-k8s-worker-2 # if not already created
```

```bash
kubectl apply -k k3s/overlays/join-k8s-worker-2 -n join-k8s-worker-2
```

Stop join-worker-2
```bash
kubectl delete all --all -n join-k8s-worker-2

# To delete pvc
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-2
```

Run join-worker-3

```bash
kubectl create namespace join-k8s-worker-3 # if not already created
kubectl apply -k k3s/overlays/join-k8s-worker-3 -n join-k8s-worker-3
```

Stop join-worker-3
```bash
kubectl delete all --all -n join-k8s-worker-3

# To delete pvc
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-3
```

Clean state
```bash
gcloud compute ssh k8s-worker-1 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-2 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-3 --zone us-central1-a --command "sudo rm -rf /srv/dai"
```

Stop all
```bash
kubectl delete all --all -n genesis
kubectl delete all --all -n join-k8s-worker-2
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-2
kubectl delete all --all -n join-k8s-worker-3
kubectl delete pvc tmkms-data-pvc -n join-k8s-worker-3

gcloud compute ssh k8s-worker-1 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-2 --zone us-central1-a --command "sudo rm -rf /srv/dai"
gcloud compute ssh k8s-worker-3 --zone us-central1-a --command "sudo rm -rf /srv/dai"
```
