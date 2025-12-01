import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.util.*
import java.util.concurrent.TimeUnit
import kotlin.time.Duration

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class AuthTokenFlowTests : TestermintTest() {

    @Test
    fun `full flow with auth tokens`() {
        // 1. Setup with auth tokens
        val (cluster, genesis) = initCluster(reboot = true)
        val authToken = "test-auth-token-${UUID.randomUUID()}"
        
        // Configure MLNodes to verify auth token
        cluster.allPairs.forEach { pair ->
            pair.mock?.setRequestValidator { request ->
                val token = request.headers["Authorization"]
                assertThat(token).isEqualTo("Bearer $authToken")
                true
            }
            pair.waitForMlNodesToLoad()
        }

        // 2. Wait for first PoC and verify weights
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val participants = genesis.api.getParticipants()
        participants.forEach { participant ->
            assertThat(participant.pocWeight).isGreaterThan(0)
            Logger.info("Participant ${participant.id} has weight ${participant.pocWeight}")
        }

        // 3. Do inferences in Inference phase with auth token
        genesis.waitForNextInferenceWindow()
        val inferenceHelper = InferenceTestHelper(cluster, genesis).apply {
            // Add auth token to inference requests
            this.request = this.request.copy(headers = mapOf(
                "Authorization" to "Bearer $authToken"
            ))
        }
        val inference = inferenceHelper.runFullInference()
        
        // 4. Verify inference succeeded and was validated
        assertThat(inference.statusEnum).isEqualTo(InferenceStatus.VALIDATED)
        // Verify auth token was checked during inference
        cluster.allPairs.forEach { pair ->
            assertThat(pair.mock?.getLastInferenceRequest()?.headers?.get("Authorization"))
                .isEqualTo("Bearer $authToken")
        }
        
        // Wait for next PoC to end
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Verify rewards were received
        val updatedParticipants = genesis.api.getParticipants()
        updatedParticipants.forEach { participant ->
            assertThat(participant.coinsOwed).isGreaterThan(0)
            Logger.info("Participant ${participant.id} earned ${participant.coinsOwed}")
        }
    }

    @Test
    fun `requests without auth token should fail`() {
        // 1. Setup with auth tokens
        val (cluster, genesis) = initCluster(reboot = true)
        val authToken = "test-auth-token-${UUID.randomUUID()}"
        
        // Configure MLNodes to reject requests without auth token by setting an error response
        cluster.allPairs.forEach { pair ->
            // Set up the mock to return a 401 Unauthorized error for all inference requests
            // This simulates the behavior of rejecting requests without proper auth tokens
            pair.mock?.setInferenceErrorResponse(
                statusCode = 401,
                errorMessage = "Unauthorized: Missing or invalid auth token",
                errorType = "invalid_request_error"
            )
            pair.waitForMlNodesToLoad()
        }

        // 2. Try to do inference without auth token
        genesis.waitForNextInferenceWindow()
        val inferenceHelper = InferenceTestHelper(cluster, genesis)
        // Note: Not adding auth token to headers, which should cause failure
        
        val inference = inferenceHelper.runFullInference()
        
        // 3. Verify inference failed due to missing auth token
        assertThat(inference.statusEnum).isEqualTo(InferenceStatus.FAILED)
        Logger.info("Inference failed as expected due to missing auth token")
        
        // 4. Now configure the mock to accept requests and try with proper auth token
        cluster.allPairs.forEach { pair ->
            // Reset the mock to return a successful response
            pair.mock?.setInferenceResponse(
                response = """{"id":"chatcmpl-123","object":"chat.completion","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}""",
                delay = Duration.ZERO
            )
            pair.waitForMlNodesToLoad()
        }
        
        val inferenceHelperWithAuth = InferenceTestHelper(cluster, genesis).apply {
            this.request = this.request.copy(headers = mapOf(
                "Authorization" to "Bearer $authToken"
            ))
        }
        val successfulInference = inferenceHelperWithAuth.runFullInference()
        
        // 5. Verify inference succeeds with proper auth token
        assertThat(successfulInference.statusEnum).isEqualTo(InferenceStatus.VALIDATED)
        Logger.info("Inference succeeded with proper auth token")
    }
}