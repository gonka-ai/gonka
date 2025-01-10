set -e

# Build and push node docker containers
make -C ../. node-build-docker
make -C ../inference-chain docker-push-join

# Build and push api docker containers
make -C ../. api-build-docker
make -C ../decentralized-api docker-push-release
