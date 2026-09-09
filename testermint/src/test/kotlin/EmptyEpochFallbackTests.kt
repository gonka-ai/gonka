import com.productscience.EpochStage
import com.productscience.data.AppState
import com.productscience.data.Decimal
import com.productscience.data.EpochParams
import com.productscience.data.InferenceParams
import com.productscience.data.InferenceState
import com.productscience.data.PocParams
import com.productscience.data.StakeValidatorStatus
import com.productscience.data.getParticipant
import com.productscience.data.spec
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

/**
 * Seatbelt for an empty PoC outcome: if nobody passes validation (and nothing is
 * preserved into the next epoch), epoch formation must re-seat the current live
 * validators instead of writing an empty active set and permanently stalling.
 */
@Timeout(value = 15, unit = TimeUnit.MINUTES)
class EmptyEpochFallbackTests : TestermintTest() {

    // Tiny preservation target so IntPart(fraction * totalWeight) == 0 with the
    // default 3-validator cluster weights. That keeps PreservedParticipants empty,
    // so ComputeNewWeights can actually return [] when PoC mining also fails.
    // (pocSlotAllocation == 0 is rewritten to the 0.5 default on-chain.)
    private val noPreservationSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::pocSlotAllocation] = Decimal.fromDouble(0.001)
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = true
                }
            }
        }
    }

    private val noPreservationConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(noPreservationSpec) ?: noPreservationSpec,
    )

    @Test
    fun `empty poc validation falls back to previous epoch validators`() {
        logSection("=== TEST: Empty PoC epoch fallback ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            config = noPreservationConfig,
            reboot = true,
            resetMlNodes = false,
        )
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]
        val allPairs = listOf(genesis, join1, join2)

        logSection("Phase 1: Normal PoC — establish 3 active validators")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        val before = genesis.api.getActiveParticipants()
        val beforeAddresses = before.activeParticipants.participants.map { it.index }.toSet()
        val beforeEpochId = before.activeParticipants.epochId

        Logger.info("Active before failure epoch: epochId=$beforeEpochId addresses=$beforeAddresses")
        assertThat(beforeAddresses).hasSize(3)
        allPairs.forEach { pair ->
            assertThat(before.activeParticipants.getParticipant(pair))
                .describedAs("${pair.name} should be active after the healthy PoC")
                .isNotNull
        }

        logSection("Phase 2: Force empty PoC outcome — zero weight for every mock")
        // claimedWeight < 1 drops every mining participant. Combined with no
        // preserved nodes, ComputeNewWeights returns empty and the seatbelt fires.
        allPairs.forEach { it.setPocWeight(0) }

        logSection("Phase 3: Run the next PoC cycle with zero weight")
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("Zero-weight PoC generation started")
        genesis.waitForStage(EpochStage.END_OF_POC_VALIDATION, offset = 2)
        Logger.info("PoC validation ended — fallback should have re-seated validators")

        logSection("Phase 4: Wait for new validators / settlement after fallback")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        val after = genesis.api.getActiveParticipants()
        val afterAddresses = after.activeParticipants.participants.map { it.index }.toSet()
        val afterEpochId = after.activeParticipants.epochId

        Logger.info("Active after failure epoch: epochId=$afterEpochId addresses=$afterAddresses")
        Logger.info("Excluded after failure epoch: ${after.excludedParticipants.map { it.address }}")

        assertThat(afterEpochId)
            .describedAs("Epoch must advance; fallback forms the upcoming epoch instead of aborting")
            .isGreaterThan(beforeEpochId)

        assertThat(afterAddresses)
            .describedAs("Seatbelt must re-seat the previous live validator set")
            .isEqualTo(beforeAddresses)

        allPairs.forEach { pair ->
            val participant = after.activeParticipants.getParticipant(pair)
            assertThat(participant)
                .describedAs("${pair.name} must still be an active participant after empty PoC")
                .isNotNull
            assertThat(participant!!.weight)
                .describedAs("${pair.name} must keep positive weight after fallback re-seat")
                .isGreaterThan(0)
        }

        logSection("Phase 5: Confirm staking still has three bonded validators")
        val validators = genesis.node.getValidators().validators
        assertThat(validators).hasSize(3)
        allPairs.forEach { pair ->
            val pubKey = pair.node.getValidatorInfo().key
            val validator = validators.find { it.consensusPubkey.value == pubKey }
            assertThat(validator)
                .describedAs("${pair.name} must remain a staking validator")
                .isNotNull
            assertThat(validator!!.statusEnum)
                .describedAs("${pair.name} must stay bonded after empty-PoC fallback")
                .isEqualTo(StakeValidatorStatus.BONDED)
            assertThat(validator.tokens)
                .describedAs("${pair.name} must keep positive staking tokens")
                .isGreaterThan(0L)
        }

        // Restore healthy mocks so a reused cluster is not left in the failure mode.
        allPairs.forEach { it.setPocWeight(10) }
        genesis.markNeedsReboot()

        logSection("TEST PASSED: empty PoC fell back to the previous three validators")
    }
}
