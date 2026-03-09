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

# Quality Matrix Unit Tests (no cluster required)

The semantic cache quality gates are fully unit-tested and run without a live
testnet, chain, or GPU. Run them from the repository root:

```bash
# All semanticcache tests (37 tests, ~0.1s)
go test ./decentralized-api/semanticcache/... -v

# Only the quality matrix gate tests (gates_test.go)
go test ./decentralized-api/semanticcache/... -v -run TestAdaptive
go test ./decentralized-api/semanticcache/... -v -run TestLoopClosure
go test ./decentralized-api/semanticcache/... -v -run TestCoherenceRatio
go test ./decentralized-api/semanticcache/... -v -run TestResearchMatrix

# Quality middleware (examples/)
go test ./examples/quality-middleware/... -v
```

## What the gate tests cover (quality_matrix_research_v2.md)

| Test group | File | Research section |
|---|---|---|
| `TestAdaptiveCoherenceFloor_*` | `gates_test.go` | §9 Calibrated parameters (Gate 1) |
| `TestLoopClosureOK_*` | `gates_test.go` | §9 Loop closure (Gate 2) |
| `TestCoherenceRatioAnomaly_*` | `gates_test.go` | §10 Coherence-ratio anomaly gate |
| `TestResearchMatrix_GateSequence` | `gates_test.go` | §10 C1-C6 full gate table |
| `TestResearchMatrix_RatioGateClosesGap` | `gates_test.go` | §10 Residual gap fix |
| `TestLoopClosureBreakCounter_*` | `gates_test.go` | §9 PoC honest loop counters |
| `TestCoherenceStats_*` | `coherence_test.go` | Hub frontier accumulation |
| `TestMatrix_*` | `cache_test.go` | L1/L2/TTL/model-version matrix |
| `TestHTTP_*` | `cache_http_test.go` | X-Cache headers, integrity |

## Adversarial L2 prompts (Phase 2 compressa test)

`compressa-testing/cache-test-prompts-adversarial.csv` contains 8 pairs of
semantically similar but logically inverted prompts:

| Pair | Seed (Phase 0) | Adversarial query (Phase 2) | Error class |
|---|---|---|---|
| sort direction | sort ascending | sort descending | C4: inverted_direction |
| cache level | L1 exact-match explanation | L2 cosine explanation | C5: wrong_algorithm |
| HTTP method | POST /users | GET /users | inverted_direction |
| scan direction | find minimum | find maximum | inverted_direction |
| mutex op | increment race fix | decrement race fix | inverted_direction |
| cache eviction | LRU cache | LFU cache | wrong_algorithm |
| encoding | base64 encode | base64 decode | inverted_direction |
| recursion | Fibonacci | Tribonacci | wrong_algorithm |

**How to run the adversarial test against a live testnet:**

```bash
# Phase 0: seed adversarial prompts into cache
compressa-perf run \
  --config compressa-testing/config.yml \
  --prompts compressa-testing/cache-test-prompts-adversarial.csv \
  --tasks 80 --runners 2 --name "adversarial_seed"

# Wait one minute for cache to warm

# Phase 2: send inverted queries — odd rows are seeds, even rows are queries
# In practice the adversarial CSV interleaves pairs so compressa sends
# even-indexed prompts after the odd-indexed ones have seeded the cache.
compressa-perf run \
  --config compressa-testing/config.yml \
  --prompts compressa-testing/cache-test-prompts-adversarial.csv \
  --tasks 80 --runners 2 --name "adversarial_query"
```

After both phases check `/admin/v1/cache/stats` for:
- `loop_closure_breaks` — breaks triggered by inverted queries
- `coherence_rejections` — floor rejections (should not be high for correct queries)
- `l2_hits` — confirms the adversarial queries hit the L2 cache path

Expected: adversarial pairs that are in the **clear zone** (sim > 6250) will NOT
be caught by the current pipeline (C4/C5 residual gap). They will appear as `l2_hits`
with no `loop_closure_breaks`. This is the documented 0.5% residual error rate.

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
