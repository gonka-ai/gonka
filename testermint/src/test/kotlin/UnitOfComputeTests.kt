import com.productscience.data.UnitOfComputePriceProposalDto
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import org.junit.jupiter.api.Test

class UnitOfComputeTests : TestermintTest() {
    @Test
    fun `price proposal`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        val priceProposalResponse = instance.api.getPriceProposal()

        println(priceProposalResponse)

        instance.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 888.toULong()))

        val priceProposalResponse2 = instance.api.getPriceProposal()

        println(priceProposalResponse2)
    }
}