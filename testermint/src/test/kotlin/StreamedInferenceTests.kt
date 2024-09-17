import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequestStream
import com.productscience.stream
import org.junit.jupiter.api.Test
import java.io.*
import java.net.HttpURLConnection
import java.net.URL

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
}
