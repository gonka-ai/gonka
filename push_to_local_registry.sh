VERSION="0.0.1-multimodels-1"

docker pull gcr.io/decentralized-ai/api:"$VERSION"
docker tag gcr.io/decentralized-ai/api:"$VERSION" 172.18.114.101:5556/decentralized-ai/api:"$VERSION"
docker push 172.18.114.101:5556/decentralized-ai/api:"$VERSION"

docker pull gcr.io/decentralized-ai/inferenced:"$VERSION"
docker tag gcr.io/decentralized-ai/inferenced:"$VERSION" 172.18.114.101:5556/decentralized-ai/inferenced:"$VERSION"
docker push 172.18.114.101:5556/decentralized-ai/inferenced:"$VERSION"
