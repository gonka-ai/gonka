import com.google.gson.annotations.SerializedName
import com.productscience.EpochStage
import com.productscience.data.TxMessage
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class BLSProofPathTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    fun `verification vector true vote without proofs is rejected`() {
        logSection("Testing proof-required path for true verification vote")

        val (cluster, genesis) = initCluster(joinCount = 2, reboot = true)
        val participants = listOf(genesis) + cluster.joinPairs
        val participant = participants.first()

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val epochId = resolveEpochId(genesis)
        waitForExactPhase(genesis, epochId, "VERIFYING")

        val epochData = genesis.node.queryBLSEpochData(epochId).epochData
        val dealerCount = epochData.participants.size
        val submitterAddress = participant.node.getColdAddress()
        val submitterIndex = epochData.participants.indexOfFirst { it.address == submitterAddress }
        assertThat(submitterIndex).isNotEqualTo(-1)
        val nonSelfDealerIndex = epochData.participants.indices.first { it != submitterIndex }
        val dealerValidity = List(dealerCount) { it == nonSelfDealerIndex }

        val result = runCatching {
            participant.submitMessage(
                MsgSubmitVerificationVectorTx(
                    creator = participant.node.getColdAddress(),
                    epochId = epochId,
                    dealerValidity = dealerValidity,
                    dealerComplaints = emptyList(),
                    dealerValidityProofs = emptyList()
                )
            )
        }

        if (result.isSuccess) {
            assertThat(result.getOrThrow().code).isNotEqualTo(0)
        } else {
            val errorMessage = result.exceptionOrNull()?.message ?: ""
            assertThat(errorMessage).isNotBlank()
        }
    }

    private fun resolveEpochId(genesis: com.productscience.LocalInferencePair): Long {
        val base = genesis.getCurrentBlockHeight() / genesis.getEpochLength()
        val candidates = listOf(base, base + 1, base - 1).filter { it >= 1 }
        candidates.forEach { epochId ->
            val exists = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.isSuccess
            if (exists) return epochId
        }
        return base
    }

    private fun waitForExactPhase(
        pair: com.productscience.LocalInferencePair,
        epochId: Long,
        phaseToken: String,
        maxAttempts: Int = 35
    ) {
        repeat(maxAttempts) {
            val phase = runCatching { pair.node.queryBLSEpochData(epochId).epochData.dkgPhase }.getOrNull() ?: ""
            if (phase.contains("FAILED")) {
                error("DKG failed for epoch $epochId while waiting for $phaseToken")
            }
            if (phase.contains(phaseToken)) {
                return
            }
            pair.node.waitForNextBlock(1)
        }
        error("Timeout waiting for phase $phaseToken in epoch $epochId")
    }
}

data class MsgSubmitVerificationVectorTx(
    @field:SerializedName("@type")
    override val type: String = "/inference.bls.MsgSubmitVerificationVector",
    @field:SerializedName("creator")
    val creator: String,
    @field:SerializedName("epoch_id")
    val epochId: Long,
    @field:SerializedName("dealer_validity")
    val dealerValidity: List<Boolean>,
    @field:SerializedName("dealer_complaints")
    val dealerComplaints: List<VerificationDealerComplaintTx> = emptyList(),
    @field:SerializedName("dealer_validity_proofs")
    val dealerValidityProofs: List<DealerValidityProofTx> = emptyList()
) : TxMessage

data class VerificationDealerComplaintTx(
    @field:SerializedName("dealer_index")
    val dealerIndex: Long,
    @field:SerializedName("disputed_slot_index")
    val disputedSlotIndex: Long,
    @field:SerializedName("disputed_ciphertext_index")
    val disputedCiphertextIndex: Long
)

data class DealerValidityProofTx(
    @field:SerializedName("dealer_index")
    val dealerIndex: Long,
    @field:SerializedName("proof_signature")
    val proofSignature: ByteArray
)
