# Withdraw 

## Gonka side

### Request withdraw
```
./inferenced tx wasm execute <GONKA_WRAPPED_TOKEN_ADDRESS> \
    '{"withdraw":{"amount":"190000","destination_address":"ETH_DESTINATION_ADDRESS"}}' \
    --from testnet \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --node http://89.169.111.79:8000/chain-rpc/ \
    --gas auto \
    --gas-adjustment 1.3 \
    --yes
```

Resp:
```
Enter keyring passphrase (attempt 1/3):
gas estimate: 1288622
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 12E8ABCA5A35D73042564FDF6D686424F742414EEC172450AB6EDA34BD1F0805
```

### Get requestId

```
./inferenced query tx 12E8ABCA5A35D73042564FDF6D686424F742414EEC172450AB6EDA34BD1F0805 \
  --node http://89.169.111.79:8000/chain-rpc/ \
  --output json |
jq -r '.events[]
        | select(.type == "inference.bls.EventThresholdSigningRequested")
        | .attributes[]
        | select(.key == "request_id")
        | .value'

```

Example:
"vSTWiN1pvooxcFoDLzePCEq3x/C5NQ+jFMvfcEozCm4="

### Get hex
```
echo "vSTWiN1pvooxcFoDLzePCEq3x/C5NQ+jFMvfcEozCm4=" | base64 -d | xxd -p -c 256
bd24d688dd69be8a31705a032f378f084ab7c7f0b9350fa314cbdf704a330a6e
```

### Get signature

```
curl http://localhost:8000/v1/bls/signatures/bd24d688dd69be8a31705a032f378f084ab7c7f0b9350fa314cbdf704a330a6e \
  | jq -r '
    {
      uncompressed_signature_128: .uncompressed_signature_128,
      current_epoch_id: .signing_request.current_epoch_id,
      request_id: .signing_request.request_id
    }
  '
{
  "uncompressed_signature_128": "AAAAAAAAAAAAAAAAAAAAAAYc935pphgsWzRMEvsnf54X4AcjoWWkhgmi0chllDHMFcMxSpMIVGa48ARlGx9+RgAAAAAAAAAAAAAAAAAAAAAWf5RR5Pawp0ZSy4QHnHqdflD0fT0yr6zJ83z2ZARJyRgd56robaSWdyo4Pj1Ca2Y=",
  "current_epoch_id": 135,
  "request_id": "vSTWiN1pvooxcFoDLzePCEq3x/C5NQ+jFMvfcEozCm4="
}
```

## Etherium side

### Send BLS for epoch till current

```
HARDHAT_NETWORK=mainnet node submit-epoch-public.js \
    <ETH_CONTRACT_ADDRESS> \
    135 \
    AAAAAAAAAAAAAAAAAAAAAA63vXLd3uPbuxH1yrS/bROB2nRYHjOY9K1QVOb8aQAd+zfE8LSn+OFj78VGwGGrlQAAAAAAAAAAAAAAAAAAAAAXaSw0bnEzpP5IFI3QJzex7bkeo3hKmGUOSd6M54JxgGdztl5/C8nTtZO/+LO8TCMAAAAAAAAAAAAAAAAAAAAAEvuyXmuyhnE7T3W0yn06GV4RoUb9ptfPxotVgM+CFf6JZXDY/0JmNg0AyE/pev2EAAAAAAAAAAAAAAAAAAAAAAWeLkP67OzhmOcLlubkTnGgclH0TiRCrRRaD1DfD4eo5kPlVjtdZKRSV9ydIsLUcQ== \
    AAAAAAAAAAAAAAAAAAAAAAZ99Eu4W7Ca2A0wBDwCw+fwGG/CbseFoNNYATktzvMTStrQ8pplV06XWPmBo8IpEQAAAAAAAAAAAAAAAAAAAAAIQ18sBomMuhw5x1LwUdRMbRUudE/p8yb08DanvEkdwYr0AnQeGtmMMrwDHWz2JuA=
```

### Withdraw

```
HARDHAT_NETWORK=mainnet node withdraw-tokens.js \
  <ETH_CONTRACT_ADDRESS> \ # Contract address
  135 \ # epochId
  "vSTWiN1pvooxcFoDLzePCEq3x/C5NQ+jFMvfcEozCm4=" \ # requestId
  "0x8BF9D25F9a63764A52Bcbf8E742a475b38D3838c" \ # destinationAddress
  0xdac17f958d2ee523a2206206994597c13d831ec7 \ # USDT Contract address
  190000 \ # Amount
  "AAAAAAAAAAAAAAAAAAAAAAYc935pphgsWzRMEvsnf54X4AcjoWWkhgmi0chllDHMFcMxSpMIVGa48ARlGx9+RgAAAAAAAAAAAAAAAAAAAAAWf5RR5Pawp0ZSy4QHnHqdflD0fT0yr6zJ83z2ZARJyRgd56robaSWdyo4Pj1Ca2Y=" # Signature
```


# Mint (examples have another outdated contract)

Similar but:

```
./inferenced tx inference request-bridge-mint \
    1000000000 \
    <ETH_CONTRACT_ADDRESS> \
    "ethereum" \
    --from testnet \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --node http://89.169.111.79:8000/chain-rpc/ \
    --gas auto \
    --gas-adjustment 1.3 \
    --yes
```

And accordinly:


```bash
HARDHAT_NETWORK=mainnet node mint-wgnk.js <ETH_CONTRACT_ADDRESS> 5 \
  TCjLmj/TXQtQzM8SLLKcOB1NUidAHLi1+SrHhTj4B34= \
  <ETH_DESTINATION_ADDRESS> \
  1000000000 \
  AAAAAAAAAAAAAAAAAAAAAAaT1+fAtBplGOY8GLdhwfs0rC0qte+hrsPzbTTGKtjsW1IePQ3QmxFk7EufUNK8YgAAAAAAAAAAAAAAAAAAAAAXEuDg+U9cQzv9YKsWQXGfP1ljBECkZCv3HxrYlk6DsukefcYqyGkPsF6z8S6qH+Q=
```