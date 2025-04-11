import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequestStream
import com.productscience.stream
import org.junit.jupiter.api.Disabled
import org.junit.jupiter.api.Test
import java.io.*
import java.net.HttpURLConnection
import java.net.URL

@Disabled
class StreamedInferenceTests : TestermintTest() {

    @Test
    fun test() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        val signature = instance.node.signPayload(inferenceRequestStream)
        val address = instance.node.getAddress()
        println(signature)
        println(address)

        val streamedResponse = instance.api.makeStreamedInferenceRequest(inferenceRequestStream, address, signature)
    }

    @Test
    fun listInference() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        val output = instance.node.exec(listOf("inferenced", "query", "inference", "list-inference"))
        println(output)
    }

    @Test
    fun getInference() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        val inferenceId = "4cd1f41d-0afd-4186-8d4a-c78b50c302af"
        val output = instance.node.exec(listOf("inferenced", "query", "inference", "show-inference", inferenceId))
        println(output)
    }

    @Test
    fun runValidation() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]

        println(instance.node.getAddress())
        println(instance.api.url)

        instance.api.runValidation("4cd1f41d-0afd-4186-8d4a-c78b50c302af")
    }
}
