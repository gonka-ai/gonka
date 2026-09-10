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
 * Covered here (merge-safe with enabled_fee_groups empty at genesis):
 * 1. Fee groups ship disabled; global min_gas_price_ngonka is 0
 * 2. Governance can enable the epoch group price without changing the global field
 * 3. CLI collateral deposit rejects zero/insufficient fees and succeeds when funded
 *
 * DAPI does not read DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA for group prices.
 * After activation it prices from the on-chain fee tree (Params) and pays via
 * the cold-to-warm feegrant. The following must be run on a rehearsal cluster
 * before enabling epoch in production — they are not merge blockers while
 * enabled_fee_groups stays empty:
 * - upgrade migration from legacy flat fee fields
 * - warm-key authz StoreCommit paying through the cold-key feegrant
 * - first and incremental StoreCommit gas from canonical Count delta
 * - paid HardwareDiff plus version/host/port convergence
 * - stale-low cache recovery after an insufficient-fee CheckTx
 * - removing epoch restores coin-price-only recovery (see finding 2)
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
    fun `fee groups disabled at genesis`() {
        logHighlight("Verifying FeeParams ship with empty enabled_fee_groups")

        val params = genesis.getParams()
        assertThat(params.feeParams).isNotNull
        assertThat(params.feeParams!!.enabledFeeGroups).isEmpty()
        logHighlight("Fee groups correctly disabled at genesis")
    }

    // Pre-fee classic inference smoke test removed with the dapi deprecation:
    // classic /v1/chat/completions now returns 410. Fee-exempt bypass for
    // inference/PoC messages remains covered by unit tests in ante_fee_test.go.

    // ========== ENABLE FEES ==========

    @Test
    @Order(3)
    fun `enable fee enforcement via governance proposal`() {
        logHighlight("Enabling epoch fee group via governance")

        val params = genesis.getParams()
        val existing = params.feeParams ?: FeeParamsData()
        val groups = existing.groups?.deepCopy()
            ?: error("FeeParams must contain the configured fee groups")
        val epochGroup = groups
            .map { it.asJsonObject }
            .firstOrNull { it.get("name")?.asString == "epoch" }
            ?: error("FeeParams must contain the epoch fee group")
        epochGroup.addProperty("min_gas_price", 10)
        val paramsWithFees = params.copy(
            feeParams = existing.copy(
                enabledFeeGroups = listOf("epoch"),
                minGasPriceNgonka = 0,
                baseValidationGas = existing.baseValidationGas.takeIf { it > 0 } ?: 500_000,
                gasPerPocCount = existing.gasPerPocCount.takeIf { it > 0 } ?: 100,
                groups = groups,
            )
        )

        genesis.runProposal(cluster, UpdateParams(params = paramsWithFees))
        genesis.node.waitForNextBlock(2)
        logHighlight("Fee enforcement proposal passed")
    }

    // ========== POST-ENABLE: CLI rejection tests ==========
    // CLI attaches fees explicitly. DAPI group pricing is independent of
    // DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA (fee tree comes from chain Params).

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

    @Test
    @Order(7)
    fun `global min gas price stays zero after epoch enablement`() {
        val params = genesis.getParams()
        assertThat(params.feeParams).isNotNull
        assertThat(params.feeParams!!.minGasPriceNgonka).isEqualTo(0)
        assertThat(params.feeParams!!.enabledFeeGroups).contains("epoch")
        logHighlight("Epoch group enabled; global min_gas_price_ngonka remains 0")
    }
}
