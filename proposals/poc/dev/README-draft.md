let's carefully analyze:
@mlnode/planning/ new-poc/mlnode-integration.md 
and commit e02f585d0c7ebbb85ceb7cc0b12215c2f5ca445e

the task for integration of new poc2:
/home/ubuntu/workspace/vllm/vllm/poc/routes.py
and /home/ubuntu/workspace/vllm/planning-poc-v2/production*.md

----

Out next step would be to design the integrate the same in @decentralized-api and @inference-chain 


I expect this to be really minimalistic and easy to read integration to mlnode api, with minimal coding but without any efficiency proble @packages/api/.cursorrules/rules.md 

All curretn API, including initial pow/poc should continue exist. It'll just be additional NEW one, probably with v2 or smth like that

------
Let't decide how we'll integrate it (for context - it'll be already on living chain, so we can't remove)

At current step, i want you:
- design of new message types in proto for such artifact. smth like artifact { nonce: int32, vector: bytes }
- design minimalistic integration of the validation (do we need validation message per participant at all? or can send only big bunch already now, let's say once in couple minutes and 100 participant). In a way address: validatedWeigt, -1 if invalid 
- propose how we can make minimalistic switch, to make @testermint switch with minimal hustle (usually testermint is pretty sensitive)
- accordinlt design mock, as it used for poc in testermint now

I expect us to design this step in the way to make it really minimalistic. Like as simple switch as possible. 