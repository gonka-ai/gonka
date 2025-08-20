Account Public Key: `Auz6M2noPEepwz55meXHhx3hNGpjIglMiBXXXkQ+yOV9`
Node ID: `c637495acea140a2f76e52b20f7e80e5cb6784ef`
ML Operational Address: `gonka1f3aadttygsta8s6qasfh220ke9dggsthc6yzuh`
Consensus Public Key: `whKz8YbqOxRYnSitr91k61KPoOqh5XiTUkZVeGD7lVA=`
P2P_EXTERNAL_ADDRESS: `tcp://gonka.spv.re:5000`

./inferenced genesis gentx \
    --keyring-backend file \
    gonka-account-key 1nicoin \
    --moniker spvre \
    --pubkey whKz8YbqOxRYnSitr91k61KPoOqh5XiTUkZVeGD7lVA= \
    --ml-operational-address gonka1f3aadttygsta8s6qasfh220ke9dggsthc6yzuh \
    --url http://gonka.spv.re:8000 \
    --chain-id gonka-rehearsal \
    --node-id c637495acea140a2f76e52b20f7e80e5cb6784ef