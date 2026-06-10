import com.productscience.findStageSafeInferenceBlock
import com.productscience.data.Decimal
import com.productscience.data.EpochExchangeWindow
import com.productscience.data.EpochParams
import com.productscience.data.EpochPhase
import com.productscience.data.EpochResponse
import com.productscience.data.EpochStages
import com.productscience.data.LatestEpochDto
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class EpochSchedulingHelpersTest {
    @Test
    fun `findStageSafeInferenceBlock uses current inference window when lead fits`() {
        val epochData = epochResponse(
            blockHeight = 420,
            phase = EpochPhase.Inference,
            epochClaimMoney = 416,
            epochNextPocStart = 450,
            nextClaimMoney = 466,
            nextNextPocStart = 500,
        )

        val selected = requireNotNull(epochData.findStageSafeInferenceBlock(earliestBlock = 428))

        assertThat(selected.block).isEqualTo(428)
        assertThat(selected.inferenceWindowStart).isEqualTo(417)
        assertThat(selected.nextPocStart).isEqualTo(450)
    }

    @Test
    fun `findStageSafeInferenceBlock skips unsafe tail of current inference window`() {
        val epochData = epochResponse(
            blockHeight = 420,
            phase = EpochPhase.Inference,
            epochClaimMoney = 416,
            epochNextPocStart = 450,
            nextClaimMoney = 466,
            nextNextPocStart = 500,
        )

        val selected = requireNotNull(epochData.findStageSafeInferenceBlock(earliestBlock = 447))

        assertThat(selected.block).isEqualTo(467)
        assertThat(selected.inferenceWindowStart).isEqualTo(467)
        assertThat(selected.nextPocStart).isEqualTo(500)
    }

    @Test
    fun `findStageSafeInferenceBlock anchors on upcoming inference window when currently in validation`() {
        val epochData = epochResponse(
            blockHeight = 433,
            phase = EpochPhase.PoCValidate,
            epochClaimMoney = 440,
            epochNextPocStart = 460,
            nextClaimMoney = 476,
            nextNextPocStart = 510,
        )

        val selected = requireNotNull(epochData.findStageSafeInferenceBlock(earliestBlock = 445))

        assertThat(selected.block).isEqualTo(445)
        assertThat(selected.inferenceWindowStart).isEqualTo(441)
        assertThat(selected.nextPocStart).isEqualTo(460)
    }

    @Test
    fun `findStageSafeInferenceBlock projects forward beyond the next epoch when lead is long`() {
        val epochData = epochResponse(
            blockHeight = 363,
            phase = EpochPhase.Inference,
            epochClaimMoney = 358,
            epochNextPocStart = 390,
            nextClaimMoney = 398,
            nextNextPocStart = 430,
        )

        val selected = requireNotNull(epochData.findStageSafeInferenceBlock(earliestBlock = 443))

        assertThat(selected.block).isEqualTo(443)
        assertThat(selected.inferenceWindowStart).isEqualTo(439)
        assertThat(selected.nextPocStart).isEqualTo(470)
    }

    private fun epochResponse(
        blockHeight: Long,
        phase: EpochPhase,
        epochClaimMoney: Long,
        epochNextPocStart: Long,
        nextClaimMoney: Long,
        nextNextPocStart: Long,
    ): EpochResponse {
        return EpochResponse(
            blockHeight = blockHeight,
            latestEpoch = LatestEpochDto(index = 11, pocStartBlockHeight = 400),
            phase = phase,
            epochStages = epochStages(
                epochIndex = 11,
                claimMoney = epochClaimMoney,
                nextPocStart = epochNextPocStart,
            ),
            nextEpochStages = epochStages(
                epochIndex = 12,
                claimMoney = nextClaimMoney,
                nextPocStart = nextNextPocStart,
            ),
            epochParams = EpochParams(
                epochLength = 60,
                epochMultiplier = 1,
                epochShift = 0,
                defaultUnitOfComputePrice = 100,
                pocStageDuration = 10,
                pocExchangeDuration = 1,
                pocValidationDelay = 2,
                pocValidationDuration = 4,
                setNewValidatorsDelay = 1,
                inferenceValidationCutoff = 1,
                inferencePruningEpochThreshold = 10,
                inferencePruningMax = 100,
                pocPruningMax = 100,
                pocSlotAllocation = Decimal.fromDouble(0.5),
                confirmationPocSafetyWindow = 0,
            ),
            isConfirmationPocActive = false,
            activeConfirmationPocEvent = null,
        )
    }

    private fun epochStages(
        epochIndex: Long,
        claimMoney: Long,
        nextPocStart: Long,
    ): EpochStages {
        return EpochStages(
            epochIndex = epochIndex,
            pocStart = claimMoney - 20,
            pocGenerationWindDown = claimMoney - 16,
            pocGenerationEnd = claimMoney - 15,
            pocValidationStart = claimMoney - 10,
            pocValidationWindDown = claimMoney - 6,
            pocValidationEnd = claimMoney - 5,
            setNewValidators = claimMoney - 1,
            claimMoney = claimMoney,
            nextPocStart = nextPocStart,
            pocExchangeWindow = EpochExchangeWindow(
                start = claimMoney - 14,
                end = claimMoney - 11,
            ),
            pocValExchangeWindow = EpochExchangeWindow(
                start = claimMoney - 9,
                end = claimMoney - 7,
            ),
        )
    }
}
