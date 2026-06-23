# decentralized-api

The API node: the controller that registers, manages, and routes requests to
MLNodes on behalf of a network participant. This glossary fixes the language for
how the API node talks about MLNodes and how it reaches them.

## Language

**MLNode**:
An external ML inference container (vLLM plus a management API, fronted by an
nginx proxy for version routing). The thing the API node registers, monitors,
and sends inference/PoC work to.
_Avoid_: ml-node, inference container, worker

**InferenceNodeConfig**:
The persisted/config representation of one MLNode (koanf + SQLite). The source of
truth for a node's identity and how to reach it, as entered by an operator.

**Node** (`broker.Node`):
The broker's in-memory, runtime representation of one MLNode, built from an
InferenceNodeConfig at registration. Carries runtime-only fields (e.g. NodeNum).
_Avoid_: using bare "node" for the MLNode itself — the MLNode is the container;
the Node is the API node's record of it.

**Endpoint** (`mlnode.Endpoint`):
How the API node reaches one MLNode: its address plus optional authentication,
and the rule for turning those into versioned PoC / inference / health URLs. A
value object (its own leaf package `mlnode`) with two mutually-exclusive variants
(Host-Port, BaseURL); illegal "both" states are unrepresentable by construction.
Produced from an InferenceNodeConfig or a Node via `.Endpoint()`; owns all MLNode
URL construction (the former `apiconfig.MLNodeURL` lives here now).
_Avoid_: MLNodeEndpoint (stutters as a package type), address (drops auth),
target, connection

**Host-Port mode**:
The Endpoint variant addressing an MLNode by host + two ports (PoC port and
inference port) with optional path segments. Health is checked at the inference
port. The original, default registration method.
_Avoid_: legacy mode, IP mode

**BaseURL mode**:
The Endpoint variant addressing an MLNode by a single stable base URL (FQDN) with
an optional bearer auth token, where one endpoint serves both management and
inference (single-port). Health is checked at `<baseURL>/readyz`. For
managed/cloud MLNodes whose IP is not stable.
_Avoid_: FQDN mode, token mode, URL mode
