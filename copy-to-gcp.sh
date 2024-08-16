# Clean existing
sudo rm docker-compose-cloud.yml
sudo rm -rf .inference

# Copy
gscp docker-compose-cloud.yml requester-node:~/docker-compose-cloud.yml
gscp docker-compose-cloud.yml executor-node:~/docker-compose-cloud.yml
gscp docker-compose-cloud.yml validator-node:~/docker-compose-cloud.yml

gscp --recurse ./gcp-prod-sim/requester requester-node:~/.inference
gscp --recurse ./gcp-prod-sim/executor executor-node:~/.inference
gscp --recurse ./gcp-prod-sim/validator validator-node:~/.inference
