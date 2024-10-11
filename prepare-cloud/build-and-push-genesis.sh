make -C ../. node-build-genesis
make -C ../inference-chain docker-push-genesis

make -C ../. api-release-docker
make -C ../decentralized-api docker-push-release
