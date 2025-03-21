import com.productscience.*
import org.junit.jupiter.api.Test

class MultiModelTests : TestermintTest() {
    @Test
    fun test() {
        val pairs = getLocalInferencePairs(inferenceConfig)

        val response = pairs[0].makeInferenceRequest(inferenceRequestModel2)
        println("RESPONSE1 = $response")
        val response2 = pairs[0].makeInferenceRequest(inferenceRequestNoSuchModel)
        println("RESPONSE2 = $response2")
        val response3 = pairs[0].makeInferenceRequest(inferenceRequest)
        println("RESPONSE3 = $response3")
    }
}