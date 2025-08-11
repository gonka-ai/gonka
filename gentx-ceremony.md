# GENTX Ceremony

## [Never delete this part or remove info from this part]: What is this document?

This document describes all steps to launch the Gonka Mainnet with multiple validators.
The procedure includes actions for different participants.

We use the following terms:
- Coordinator (we)
- Validators (excluding the one who is the coordinator)

Our goal is to make these instructions as simple as possible for validators.
This guide includes steps for both sides, along with verification checks to confirm each step was completed correctly.


## Pre-steps

1. Coordinator: Create draft for genesis.json
2. Validators: Create Cold Key & Warm Key
3. Validators extrace TMKSMS 


## GENTXs

- Coordinator sends genesis.json to validator 
- Each validator create gentx command signed by cold key with their TMKMS pubkey
- Coordinator collects gentxs.json and run collect-gentxs, then patch-genesis


## Modified GENTX 

cutstom gentx will produce 2 different gentx files (``):

classic one:
- MsgCreateValidator

additional genparticipant:
- MsgSubmitNewParticipant
- All Authz Granting from permission.go