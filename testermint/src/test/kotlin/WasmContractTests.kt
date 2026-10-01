import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

/**
 * End-to-end coverage for the `reward-splitter` contract: upload it,
 * instantiate it with a fixed payee list, fund it and check that a
 * permissionless `distribute` splits the balance by shares.
 *
 * Requires the artifact to be built first:
 *   cd inference-chain/contracts/reward-splitter && ./build.sh
 */
@Timeout(value = 15, unit = TimeUnit.MINUTES)
class WasmContractTests : TestermintTest() {

    companion object {
        private const val CONTRACT_DIR = "reward-splitter"
        private const val ARTIFACT = "reward_splitter.wasm"

        /**
         * `x/restrictions` blocks user-to-user transfers until
         * `restriction_end_block` (1555000 in the shipped genesis). A contract
         * account is a BaseAccount, not a ModuleAccount, so both funding a
         * splitter and its own `BankMsg::Send` payouts count as user-to-user
         * and are rejected. Switch the restriction off so these tests exercise
         * the contract rather than the bootstrap policy.
         */
        private val wasmConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(
                spec {
                    this[AppState::restrictions] = spec<RestrictionsState> {
                        this[RestrictionsState::params] = spec<RestrictionsParams> {
                            this[RestrictionsParams::restrictionEndBlock] = 0L
                        }
                    }
                }
            )
        )
    }

    @Test
    fun `splitter pays out by shares and keeps the rounding dust`() {
        val (_, genesis) = initCluster(config = wasmConfig)

        logSection("Install contract artifact into the node container")
        val containerPath = genesis.installWasmArtifact(contractArtifactPath(CONTRACT_DIR, ARTIFACT))

        logSection("Store wasm code")
        val storeTx = genesis.node.storeWasmCode(containerPath)
        assertThat(storeTx.code).`as`("store code tx: ${storeTx.rawLog}").isEqualTo(0)
        val codeId = storeTx.getCodeId()
        assertThat(codeId).`as`("store_code event carries a code_id").isNotNull()

        logSection("Instantiate a 7:3 split with no admin")
        val suffix = System.currentTimeMillis()
        val alpha = genesis.node.createKey("payee-alpha-$suffix").address
        val beta = genesis.node.createKey("payee-beta-$suffix").address
        val instantiateTx = genesis.node.instantiateWasmContract(
            codeId = codeId!!,
            initMsg = """{"payees":[{"address":"$alpha","shares":7},{"address":"$beta","shares":3}]}""",
            label = "reward-splitter-$suffix",
            noAdmin = true,
        )
        assertThat(instantiateTx.code).`as`("instantiate tx: ${instantiateTx.rawLog}").isEqualTo(0)
        val splitter = instantiateTx.getContractAddress()
        assertThat(splitter).`as`("instantiate event carries a contract address").isNotNull()

        logSection("Verify the immutable split")
        val split = genesis.node.queryWasmSmart<SplitQueryResponse>(splitter!!, """{"split":{}}""").data
        assertThat(split.totalShares).isEqualTo(10L)
        assertThat(split.payees.map { it.address }).containsExactly(alpha, beta)
        assertThat(split.payees.map { it.shares }).containsExactly(7L, 3L)

        logSection("Fund the splitter with an amount that does not divide evenly")
        // 1_000_003 over 7:3 leaves 3 base units of dust on the contract.
        val funding = 1_000_003L
        val fundTx = genesis.submitTransaction(
            listOf("bank", "send", genesis.node.getColdAddress(), splitter, "$funding${genesis.node.config.denom}")
        )
        assertThat(fundTx.code).`as`("funding tx: ${fundTx.rawLog}").isEqualTo(0)
        assertThat(genesis.getBalance(splitter)).isEqualTo(funding)

        val alphaBefore = genesis.getBalance(alpha)
        val betaBefore = genesis.getBalance(beta)

        logSection("Distribute")
        val distributeTx = genesis.node.executeWasmContract(splitter, """{"distribute":{}}""")
        assertThat(distributeTx.code).`as`("distribute tx: ${distributeTx.rawLog}").isEqualTo(0)

        assertThat(genesis.getBalance(alpha) - alphaBefore)
            .`as`("alpha receives floor(7/10)")
            .isEqualTo(700_002L)
        assertThat(genesis.getBalance(beta) - betaBefore)
            .`as`("beta receives floor(3/10)")
            .isEqualTo(300_000L)
        assertThat(genesis.getBalance(splitter))
            .`as`("the floor-division remainder stays on the contract")
            .isEqualTo(1L)
    }

    @Test
    fun `distributing an empty splitter is a no-op rather than a failure`() {
        val (_, genesis) = initCluster(config = wasmConfig)

        val containerPath = genesis.installWasmArtifact(contractArtifactPath(CONTRACT_DIR, ARTIFACT))

        logSection("Store and instantiate an unfunded splitter")
        val storeTx = genesis.node.storeWasmCode(containerPath)
        assertThat(storeTx.code).`as`("store code tx: ${storeTx.rawLog}").isEqualTo(0)

        val suffix = System.currentTimeMillis()
        val payee = genesis.node.createKey("payee-empty-$suffix").address
        val instantiateTx = genesis.node.instantiateWasmContract(
            codeId = storeTx.getCodeId()!!,
            initMsg = """{"payees":[{"address":"$payee","shares":1}]}""",
            label = "reward-splitter-empty-$suffix",
            noAdmin = true,
        )
        assertThat(instantiateTx.code).`as`("instantiate tx: ${instantiateTx.rawLog}").isEqualTo(0)

        logSection("Distribute with a zero balance")
        val distributeTx = genesis.node
            .executeWasmContract(instantiateTx.getContractAddress()!!, """{"distribute":{}}""")
        assertThat(distributeTx.code)
            .`as`("an empty splitter must not abort a batched crank: ${distributeTx.rawLog}")
            .isEqualTo(0)
    }
}
