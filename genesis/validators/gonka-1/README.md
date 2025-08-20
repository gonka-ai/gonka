Account Public Key: `AgOaIoGHIEb2t+Uj/iYPzWpVxgUfgPKKyLxH1mjm1nxC`  
Node ID: `44f7c802202a7aed0c6a7facff7bf009268c28eb`  
ML Operational Address: `gonka1lg4g9kakfm8ejcr8ynlnkm9qt969euswjdecyr`  
Consensus Public Key: `xMt5hWs72kqZbJDEg0U6oZ8x0p3lRXweJBUbWonND5I=`  
P2P_EXTERNAL_ADDRESS: `tcp://89.169.103.180:5000`

./inferenced genesis gentx \
    --keyring-backend file \
    gonka-1 1nicoin \
    --moniker gonka-1 \
    --pubkey xMt5hWs72kqZbJDEg0U6oZ8x0p3lRXweJBUbWonND5I= \
    --ml-operational-address gonka1lg4g9kakfm8ejcr8ynlnkm9qt969euswjdecyr \
    --url http://89.169.103.180:8000 \
    --chain-id gonka-rehearsal \
    --node-id 44f7c802202a7aed0c6a7facff7bf009268c28eb \
    --home ~/.testnet