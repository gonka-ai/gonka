import com.productscience.data.ModelPriceDto
import com.productscience.data.RegisterModelDto
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

        println("test response = $priceProposalResponse")

        instance.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 127.toULong(), denom = "uicoin"))

        val priceProposalResponse2 = instance.api.getPriceProposal()

        println("test response = $priceProposalResponse2")

        val instance2 = pairs[1]

        instance2.api.submitPriceProposal(UnitOfComputePriceProposalDto(price = 888.toULong(), denom = "uicoin"))
        instance2.api.registerModel(RegisterModelDto(id = "llama", unitsOfComputePerToken = 10.toULong()))
    }
}