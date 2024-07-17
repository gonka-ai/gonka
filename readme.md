# Inference-Ignite

## inference-chain
This is the chain as created using the Ignite CLI. It is a blockchain built using Cosmos SDK and Tendermint.

You start it by:
```shell
cd inference-chain
ignite chain server --reset-once
```
Omit `--reset-once` if you want to keep the chain data between restarts.

## decentralized-api
This is the wrapper around OpenAI API's that forwards requests to inference nodes while registering them with the inference blockchain.
Start it from it's `main.go` file, and then you can send inference requests to it:
```shell
curl --location 'http://localhost:8080/v1/chat/completions' \
--header 'Content-Type: application/json' \
--header 'Authorization: Bearer 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' \
--data '{
  "temperature" : 0.8,
  "messages": [{
      "role": "system",
      "content": "Regardless of the language of the question, answer in english"
    },
    {
        "role": "user",
        "content": "When did Hawaii become a state"
    }
  ],
  "seed" : -25
}'
```
You'll need to add valid inference nodes (local or remote) in the `config.yaml` file.

