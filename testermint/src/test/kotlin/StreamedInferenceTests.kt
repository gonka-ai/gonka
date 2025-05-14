import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequestStream
import com.productscience.initCluster
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
        val (_, instance) = initCluster()
        val signature = instance.node.signPayload(inferenceRequestStream)
        val address = instance.node.getAddress()
        println(signature)
        println(address)

        val streamedResponse = instance.api.makeStreamedInferenceRequest(inferenceRequestStream, address, signature)
    }

    @Test
    fun listInference() {
        val (_, instance) = initCluster()

        val output = instance.node.exec(listOf("inferenced", "query", "inference", "list-inference"))
        println(output)
    }

    @Test
    fun getInference() {
        val (_, instance) = initCluster()

        val inferenceId = "4cd1f41d-0afd-4186-8d4a-c78b50c302af"
        val output = instance.node.exec(listOf("inferenced", "query", "inference", "show-inference", inferenceId))
        println(output)
    }
}
