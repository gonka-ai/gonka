local('./init-prod-sim.sh')

docker_build('decentralized-api', '.', dockerfile='./decentralized-api/Dockerfile')
docker_build('inferenced', './inference-chain')

docker_compose('./docker-compose-sim.yml')