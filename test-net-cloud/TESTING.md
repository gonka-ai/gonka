# Deploy Test Net Cloud

To deploy, use the following GitHub Action workflow:

https://github.com/gonka-ai/gonka/actions/workflows/deploy-test-net-cloud.yml

# Configuring k8s

To view logs and run commands on the cluster you need to configure your local kubectl to connect to the cluster. 
Do the following steps:

1. Install kubectl
    ```bash
    brew install kubectl
    # Verify installation
    kubectl version --client
    ```
2. Copy the content of `/etc/rancher/k3s/k3s.yaml` from the control plane.
   If it does not exist, create the directory:
    ```bash
    mkdir -p ~/.kube
    ```
   Then copy the file from the control plane:
    ```bash
    gcloud compute scp dev@k8s-control-plane:/etc/rancher/k3s/k3s.yaml ~/.kube/k3s-config
    ```
3. Tunnel to the machine:
    ```bash
   gcloud compute ssh dev@k8s-control-plane -- -L 6443:localhost:6443
   ```
    While you work with `kubectl` or `stern` the tunnel needs to be alive! 
If you are getting connection errors you may need to re-establish the tunnel.
You may want to add aliases from utils.sh to your local env for managing the tunnel.

4. Test everything works:
    ```bash
    export KUBECONFIG=~/.kube/k3s-config ; kubectl get nodes
    ```

# Browsing logs

Install `stern` to browse logs from the cluster:
```bash
brew install stern
```

Command examples:
```bash
# api and node logs of genesis participant. (api|node) is a regex to match the pod names.
stern -n genesis '(api|node)' 

# Other participants:
stern -n join-k8s-worker-2 '(api|node)' 
stern -n join-k8s-worker-3 '(api|node)' 

# Look for errors. Include accepts any string and does a match on log lines. There's also exclude
stern -n genesis '(api|node)'  --include ERR

# Brows ml node logs for the genesis participant
stern -n genesis 'inference'
```

# Stress tests

To run the tests you will need the compressa tool:
```bash
# Prerequisite, create and activate venv for compressa [Optional]
python3 -m venv compressa-venv
source compressa-venv/bin/activate

# Install the compressa
pip install git+https://github.com/product-science/compressa-perf.git
```

Then see `compressa-testing/comressa-how-to.sh` for more examples.

# More useful cluster usage commands

```bash
# To run a query or any other command use kubectl exec:
kubectl -n genesis exec node-0 -- inferenced query inference list-inference --output json

kubectl -n genesis exec node-0 -- inferenced query inference params --output json

kubectl -n genesis exec node-0 -- inferenced query bank balances gonka1mfyq5pe9z7eqtcx3mtysrh0g5a07969zxm6pfl --output json

# How to tunnel to admin API, might be useful to check node status
kubectl port-forward -n genesis svc/api 9200:9200

# Then you can check ml node status at http://localhost:9200/admin/v1/nodes
```
