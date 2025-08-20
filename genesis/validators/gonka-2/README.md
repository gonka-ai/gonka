Account Public Key: `A+t/ala7HFwLn7fWFLRgwM62HspfLAitC2lSQPmeUJOv`  
Node ID: `dbca3846804a0495850048e35898b97c60fb89ab`  
ML Operational Address: `gonka14qgett9lfv6plx00jxewn8ds53r0dpac05sztu`  
Consensus Public Key: `rYIx602qGNshZ09jtBA/b3GwQ5WYfhBMDWeXTKs58tc=`  
P2P_EXTERNAL_ADDRESS: `tcp://195.242.13.239:5000`  


./inferenced genesis gentx \
    --keyring-backend file \
    gonka-2 1nicoin \
    --moniker gonka-2 \
    --pubkey rYIx602qGNshZ09jtBA/b3GwQ5WYfhBMDWeXTKs58tc= \
    --ml-operational-address gonka14qgett9lfv6plx00jxewn8ds53r0dpac05sztu \
    --url http://195.242.13.239:8000 \
    --chain-id gonka-rehearsal \
    --node-id dbca3846804a0495850048e35898b97c60fb89ab \
    --home ~/.testnet