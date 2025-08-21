import com.productscience.data.ModelPriceDto
import com.productscience.data.RegisterModelDto
import com.productscience.data.UnitOfComputePriceProposalDto
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test

class UnitOfComputeTests : TestermintTest() {
    @Test
    @Tag("unstable")
    fun `price proposal`() {
        val (cluster, instance) = initCluster()
        val pairs = cluster.allPairs

        val priceProposalResponse = instance.api.getPriceProposal()

        println("test response = $priceProposalResponse")

        instance.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 127.toULong(), denom = "ngonka"))

        val priceProposalResponse2 = instance.api.getPriceProposal()

        println("test response = $priceProposalResponse2")

        val instance2 = pairs[1]

        instance2.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 888.toULong(), denom = "ngonka"))

        val instance3 = pairs[2]
        instance3.api.registerModel(RegisterModelDto(id = "llama", unitsOfComputePerToken = 10.toULong()))
    }

    @Test
    @Tag("unstable")
    fun `submit register model proposal`() {
        val (_, instance) = initCluster()

        instance.api.registerModel(RegisterModelDto(id = "llama", unitsOfComputePerToken = 10.toULong()))
    }

    @Test
    @Tag("unstable")
    fun `vote on model proposal`() {
        val (cluster, instance) = initCluster()

        val pairs = cluster.allPairs

        pairs.forEachIndexed { i, p ->
            p.voteOnProposal("1", "yes")
        }
    }

    @Test
    @Tag("unstable")
    fun queries() {
        val (cluster, _) = initCluster()
        val pairs = cluster.allPairs
        pairs[2].node.exec(listOf("inferenced", "query", "gov", "deposits", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "params"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "proposal", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "votes", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "proposals"))
    }
}

/*
2025/02/05 07:35:27 INFO Event received event="{JSONRPC:2.0 ID:1 Result:{Query:tm.event='Tx' Data:{Type:tendermint/event/Tx Value:map[TxResult:map[height:45 result:map[data:Ei4KKC9jb3Ntb3MuZ292LnYxLk1zZ1N1Ym1pdFByb3Bvc2FsUmVzcG9uc2USAggB events:[map[attributes:[map[index:true key:fee value:] map[index:true key:fee_payer value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr]] type:tx] map[attributes:[map[index:true key:acc_seq value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr/10]] type:tx] map[attributes:[map[index:true key:signature value:Fq9qfrqTeILE2czjyClegjOPTQuEcXNh/92Sgj60+RES/UPbNkK+ZokTZuowescFfOWGtahFyHFOK8yMHT1JmQ==]] type:tx] map[attributes:[map[index:true key:action value:/cosmos.gov.v1.MsgSubmitProposal] map[index:true key:sender value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:module value:gov] map[index:true key:msg_index value:0]] type:message] map[attributes:[map[index:true key:proposal_id value:1] map[index:true key:proposal_proposer value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:proposal_messages value:,/inference.inference.MsgRegisterModel] map[index:true key:msg_index value:0]] type:submit_proposal] map[attributes:[map[index:true key:spender value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:amount value:1000000nicoin] map[index:true key:msg_index value:0]] type:coin_spent] map[attributes:[map[index:true key:receiver value:cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] map[index:true key:amount value:1000000nicoin] map[index:true key:msg_index value:0]] type:coin_received] map[attributes:[map[index:true key:recipient value:cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] map[index:true key:sender value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:amount value:1000000nicoin] map[index:true key:msg_index value:0]] type:transfer] map[attributes:[map[index:true key:sender value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:msg_index value:0]] type:message] map[attributes:[map[index:true key:depositor value:cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] map[index:true key:amount value:1000000nicoin] map[index:true key:proposal_id value:1] map[index:true key:msg_index value:0]] type:proposal_deposit]] gas_used:114914 gas_wanted:1000000000] tx:CpECCo4CCiAvY29zbW9zLmdvdi52MS5Nc2dTdWJtaXRQcm9wb3NhbBLpAQphCiUvaW5mZXJlbmNlLmluZmVyZW5jZS5Nc2dSZWdpc3Rlck1vZGVsEjgKLWNvc21vczEwZDA3eTI2NWdtbXV2dDR6MHc5YXc4ODBqbnNyNzAwajZ6bjlrbhIFbGxhbWEYChIRCgZuaWNvaW4SBzEwMDAwMDAaLWNvc21vczEzMjY4NDQ1NWtxdmVncjh5d3Y2eDM2MnJ4ZnFxM3lzaDZ2MDd6ciIbTWFkZSBmcm9tIGRlY2VudHJhbGl6ZWQtYXBpKgtteS1wcm9wb3NhbDIWSGV5IHRoaXMgaXMgYSBwcm9wb3NhbDgBEloKUApGCh8vY29zbW9zLmNyeXB0by5zZWNwMjU2azEuUHViS2V5EiMKIQOvmR/8SlbY2LZDUv59x881X6xAVMhY3fFKm9V3BzxcQhIECgIIARgKEgYQgJTr3AMaQBavan66k3iCxNnM48gpXoIzj00LhHFzYf/dkoI+tPkREv1D2zZCvmaJE2bqMHrHBXzlhrWoRchxTivMjB09SZk=]]} Events:map[coin_received.amount:[1000000nicoin] coin_received.msg_index:[0] coin_received.receiver:[cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] coin_spent.amount:[1000000nicoin] coin_spent.msg_index:[0] coin_spent.spender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] message.action:[/cosmos.gov.v1.MsgSubmitProposal] message.module:[gov] message.msg_index:[0 0] message.sender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] proposal_deposit.amount:[1000000nicoin] proposal_deposit.depositor:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] proposal_deposit.msg_index:[0] proposal_deposit.proposal_id:[1] submit_proposal.msg_index:[0] submit_proposal.proposal_id:[1] submit_proposal.proposal_messages:[,/inference.inference.MsgRegisterModel] submit_proposal.proposal_proposer:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tm.event:[Tx] transfer.amount:[1000000nicoin] transfer.msg_index:[0] transfer.recipient:[cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] transfer.sender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tx.acc_seq:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr/10] tx.fee:[] tx.fee_payer:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tx.hash:[D0F4CEF780886A92DBB01D0951A7EF00B639B7D0FD9369ADFBF2CFAD76C3E349] tx.height:[45] tx.signature:[Fq9qfrqTeILE2czjyClegjOPTQuEcXNh/92Sgj60+RES/UPbNkK+ZokTZuowescFfOWGtahFyHFOK8yMHT1JmQ==]]}}"

2025/02/05 07:35:27 INFO Event received actions=[/cosmos.gov.v1.MsgSubmitProposal] events="map[coin_received.amount:[1000000nicoin] coin_received.msg_index:[0] coin_received.receiver:[cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] coin_spent.amount:[1000000nicoin] coin_spent.msg_index:[0] coin_spent.spender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] message.action:[/cosmos.gov.v1.MsgSubmitProposal] message.module:[gov] message.msg_index:[0 0] message.sender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] proposal_deposit.amount:[1000000nicoin] proposal_deposit.depositor:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] proposal_deposit.msg_index:[0] proposal_deposit.proposal_id:[1] submit_proposal.msg_index:[0] submit_proposal.proposal_id:[1] submit_proposal.proposal_messages:[,/inference.inference.MsgRegisterModel] submit_proposal.proposal_proposer:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tm.event:[Tx] transfer.amount:[1000000nicoin] transfer.msg_index:[0] transfer.recipient:[cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn] transfer.sender:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tx.acc_seq:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr/10] tx.fee:[] tx.fee_payer:[cosmos132684455kqvegr8ywv6x362rxfqq3ysh6v07zr] tx.hash:[D0F4CEF780886A92DBB01D0951A7EF00B639B7D0FD9369ADFBF2CFAD76C3E349] tx.height:[45] tx.signature:[Fq9qfrqTeILE2czjyClegjOPTQuEcXNh/92Sgj60+RES/UPbNkK+ZokTZuowescFfOWGtahFyHFOK8yMHT1JmQ==]]"

 */
