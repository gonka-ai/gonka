import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder

/**
 * Integration tests for transaction fee enforcement lifecycle.
 *
 * Tests the full flow:
 * 1. Verify no fee enforcement at genesis (FeeParams nil)
 * 2. Verify inference works before fees
 * 3. Enable fee enforcement via governance proposal (simulates v0.2.12 upgrade)
 * 4. Verify fee-required messages are rejected without sufficient fees (via CLI)
 * 5. Verify fee-required messages succeed with sufficient fees (via CLI)
 *
 * Note: Post-enablement inference/PoC tests are not included because the DAPI
 * containers cannot be reconfigured with gas prices mid-test. Fee-exempt bypass
 * for inference and PoC messages is covered by unit tests in ante_fee_test.go.
 * MsgClaimRewards (fee-required) will fail from the DAPI after fees are enabled
 * since the DAPI has min_gas_price_ngonka=0 — this is expected and matches the
 * production rollout where DAPI config is updated alongside the upgrade.
 */
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class TransactionFeeTests : TestermintTest() {

    companion object {
        private lateinit var cluster: LocalCluster
        private lateinit var genesis: LocalInferencePair
        private lateinit var genesisAddress: String

        @BeforeAll
        @JvmStatic
        fun initOnce() {
            val result = initCluster()
            cluster = result.first
            genesis = result.second
            genesisAddress = genesis.node.getColdAddress()
        }
    }

    // ========== PRE-UPGRADE ==========

    @Test
    @Order(1)
    fun `fee params are nil at genesis`() {
        logHighlight("Verifying FeeParams are not set at genesis")

        val params = genesis.getParams()
        assertThat(params.feeParams).isNull()
        logHighlight("FeeParams correctly nil at genesis")
    }

    @Test
    @Order(2)
    fun `inference succeeds before fee enablement`() {
        logHighlight("Testing that inference works before fee enforcement is enabled")

        // Past set_new_validators so GetRandomExecutor uses all participants, not the
        // preserved-node PoC filter (which is empty right at that boundary).
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 2)
        val response = genesis.makeInferenceRequest(inferenceRequest)

        assertThat(response.choices).isNotEmpty
        logHighlight("Inference succeeded pre-fees: model=${response.model}")
    }

    // ========== ENABLE FEES ==========

    @Test
    @Order(3)
    fun `enable fee enforcement via governance proposal`() {
        logHighlight("Enabling fee enforcement via governance (simulates v0.2.12 upgrade)")

        val params = genesis.getParams()
        val paramsWithFees = params.copy(
            feeParams = FeeParamsData(
                minGasPriceNgonka = 10,
                baseValidationGas = 500_000,
                gasPerPocCount = 100,
            )
        )

        genesis.runProposal(cluster, UpdateParams(params = paramsWithFees))
        genesis.node.waitForNextBlock(2)
        logHighlight("Fee enforcement proposal passed")
    }

    // ========== POST-UPGRADE: CLI rejection tests ==========
    // These use the CLI directly (not the DAPI) so they work even though
    // the DAPI containers don't have gas prices configured.

    @Test
    @Order(4)
    fun `zero-fee collateral deposit rejected`() {
        logHighlight("Testing zero-fee collateral deposit is rejected")

        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "0ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Zero-fee collateral deposit rejected: code=${result.code}")
    }

    @Test
    @Order(5)
    fun `insufficient fee rejected`() {
        logHighlight("Testing insufficient fee is rejected")

        // At 10 ngonka/gas and 200k gas, minimum fee is 2,000,000 ngonka.
        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "1ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Insufficient fee rejected: code=${result.code}")
    }

    @Test
    @Order(6)
    fun `sufficient fee succeeds and deducts balance`() {
        logHighlight("Testing sufficient-fee collateral deposit succeeds")

        val balanceBefore = genesis.getBalance(genesisAddress)

        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "5000000ngonka"
        )

        assertThat(result.code).isEqualTo(0)

        val balanceAfter = genesis.getBalance(genesisAddress)
        val deducted = balanceBefore - balanceAfter
        assertThat(deducted).isGreaterThanOrEqualTo(1_000_000 + 5_000_000)
        logHighlight("Balance deducted: $deducted ngonka (collateral=1M + fee=5M)")
    }

    // Note: Post-upgrade DAPI inference/PoC tests are intentionally omitted
    // from this suite. The integration test containers do not configure
    // DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA, so after fees are enabled
    // via governance proposal, the DAPI's fee-required messages (reward
    // claims, hardware diffs, seeds, PoC commits) fail because the DAPI
    // declares zero fees.
    //
    // In production, hosts set DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=10 in
    // config.env before the upgrade activates. The feegrant allowance from
    // cold to warm (set up by grant-ml-ops-permissions) routes the fee
    // payment from the unfunded warm key to the funded cold account.
    //
    // The DAPI feegrant routing is covered by unit tests; the end-to-end
    // flow is documented in docs/host_onboarding.md and validated manually on
    // testnet before mainnet activation.
}
