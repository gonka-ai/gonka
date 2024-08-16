docker_build(ref='inferenced', context='./inference-chain')
docker_build(ref='decentralized-api', context='.', dockerfile='./decentralized-api/Dockerfile',
             platform='linux/arm64')

local('./init-prod-sim.sh')

docker_compose('./docker-compose-sim.yml')
