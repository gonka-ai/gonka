import com.productscience.data.RegisterModelDto
import com.productscience.data.UnitOfComputePriceProposalDto
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import org.junit.jupiter.api.Test

class UnitOfComputeTests : TestermintTest() {
    @Test
    // TODO actualize (bad request)
    fun `price proposal`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        val priceProposalResponse = instance.api.getPriceProposal()

        println("test response = $priceProposalResponse")

        instance.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 127.toULong(), denom = "uicoin"))

        val priceProposalResponse2 = instance.api.getPriceProposal()

        println("test response = $priceProposalResponse2")

        val instance2 = pairs[1]

        instance2.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 888.toULong(), denom = "uicoin"))

        val instance3 = pairs[2]
        instance3.api.registerModel(RegisterModelDto(id = "llama", unitsOfComputePerToken = 10.toULong()))
    }

    // TODO actualize (bad request)
    @Test
    fun `submit register model proposal`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[2]

        instance.api.registerModel(RegisterModelDto(id = "llama", unitsOfComputePerToken = 10.toULong()))
    }

    // TODO actualize
    @Test
    fun `vote on model proposal`() {
        val pairs = getLocalInferencePairs(inferenceConfig)

        pairs.forEachIndexed { i, p ->
            p.node.voteOnProposal("1", "yes")
        }
    }

    // TODO actualize
    @Test
    fun queries() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        pairs[2].node.exec(listOf("inferenced", "query", "gov", "deposits", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "params"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "proposal", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "votes", "1"))

        pairs[2].node.exec(listOf("inferenced", "query", "gov", "proposals"))
    }
}