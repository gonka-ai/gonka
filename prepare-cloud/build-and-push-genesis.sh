make -C ../. node-build-genesis
make -C ../inference-chain docker-push-genesis

make -C ../. node-release-docker
make -C ../inference-chain docker-push-join
