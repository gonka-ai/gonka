import com.productscience.*
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test

class MultiModelTests : TestermintTest() {
    @Test
    @Tag("unstable")
    fun test() {
        val pairs = getLocalInferencePairs(inferenceConfig)

        val response = pairs[0].makeInferenceRequest(inferenceRequestModel2)
        println("RESPONSE1 = $response")
        val response2 = pairs[0].makeInferenceRequest(inferenceRequestNoSuchModel)
        println("RESPONSE2 = $response2")
        val response3 = pairs[0].makeInferenceRequest(inferenceRequest)
        println("RESPONSE3 = $response3")
    }

    @Test
    @Tag("unstable")
    fun test2() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]
        val result = instance.node.exec(listOf("inferenced", "query", "inference", "get-random-executor", "--model=unsloth/llama-3-8b-Instruct"))
        println(result)
    }
}
