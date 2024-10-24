gcloud compute ssh --tunnel-through-iap node-genesis -- 'sudo cp ~/.inference/config/genesis.json ~/genesis.json; sudo chown $USER:$USER ~/genesis.json; sudo chmod +r ~/genesis.json'
gcloud compute scp --tunnel-through-iap node-genesis:~/genesis.json genesis.json
