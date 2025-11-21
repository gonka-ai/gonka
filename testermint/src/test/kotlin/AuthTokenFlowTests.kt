import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.util.*
import java.util.concurrent.TimeUnit

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
}