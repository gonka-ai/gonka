# GENTX Ceremony

The genesis ceremony is designed to start chain with pre-defined set of inital validators with initial genesis.json file which eveerybody agreed on.   

The process consist of several rounds to prepare initial genesis.json. All rounds are publicly available and github are publicly available.

When initial genesis.json is created, it's hash will be recorded in the blockchain and in the repository which will allow to track full state changes

> more words about ceremoney 


The ceremony is held by Coordinator which prepare initial genesis.json and collect gentxs from initial Validators
During the ceremony, Validators should provide their data via GitHub PRs.

Before the start, each Validator should make fork of that repository and create directory genesis/validators/<NAME> in [genesis/validators/](genesis/validators/) by template [genesis/validators/template](genesis/validators/template).

Details about the setup of server, key management can be found at the [Quickstart](https://gonka.ai/participant/quickstart). 


## Plan

### 1. [Validators]: Create Account Cold Key

1. Create Account Cold Key
2. Add PubKey into genesis/validators/<NAME>/README.md:

```
Account Public Key: <PubKey>
```
3. Create PR into gonka repo

### 2. [Coordinator]: Merge All PRs and Prepare `genesis.json` draft

...

### 3. [Validators]: Prepare GENTX and GENPARTICIPANT files

#### [Server]: Initialize node and get nodeId
```
docker compose run --rm node
51a9df752b60f565fe061a115b6494782447dc1f
```

#### [Server]: Get Consensus Public Key
```
docker compose up -d tmkms && docker compose run --rm --entrypoint /bin/sh tmkms -c "tmkms-pubkey"
/wTVavYr5OCiVssIT3Gc5nsfIH0lP1Rqn/zeQtq4CvQ=
```

#### [Server]: Create ML Operational Key
```
docker compose run --rm --no-deps -it api /bin/sh
address: gonka1z7w7kqukkek7n6yenwu826mqwz8yjuf2u62wm2
```

#### [Local]: Create and sign GENTX and GENPARTICIPANT files
```
./inferenced genesis gentx \
    --home ./702103 \
    --keyring-backend file \
    702103 1nicoin \
    --pubkey /wTVavYr5OCiVssIT3Gc5nsfIH0lP1Rqn/zeQtq4CvQ= \
    --ml-operational-address gonka1z7w7kqukkek7n6yenwu826mqwz8yjuf2u62wm2 \
    --url http://36.189.234.237:19256 \
    --moniker "mynode-702103" --chain-id gonka-testnet-7 \
    --node-id 51a9df752b60f565fe061a115b6494782447dc1f
```

Create PR into gonka repo with files in:
- genesis/validators/<NAME>/gentx-***.json
- genesis/validators/<NAME>/genparticipant-***.json


### [Coordinator]: Prepare final `genesis.json`
