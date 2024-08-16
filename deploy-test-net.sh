# Not a script. Just a sequence of steps I did to deploy the testnet

# 1. Log into requester
gssh requester-node
gcloud auth configure-docker

IMAGE_NAME="gcr.io/decentralized-ai/inferenced"