import com.google.gson.JsonParser
import com.productscience.cosmosJson
import com.productscience.data.AppExport
import com.productscience.genesisOverridesJson
import com.productscience.inferenceConfig
import com.productscience.voteExtensionsHeightMatches
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Test

class GenesisOverridesTest {

    private fun genesisWith(consensus: String): AppExport = cosmosJson.fromJson(
        """
        {"app_name":"inferenced","app_version":"","genesis_time":null,"initial_height":1,"app_hash":"",
         "app_state":{"bank":{},"gov":{},"inference":{},"restrictions":{}}$consensus}
        """.trimIndent(),
        AppExport::class.java,
    )

    @Test
    fun `no overrides gives empty object`() {
        assertThat(genesisOverridesJson(inferenceConfig.copy(genesisSpec = null, voteExtensionsEnableHeight = null)))
            .isEqualTo("{}")
    }

    @Test
    fun `vote extensions height is written under consensus params abci`() {
        val json = JsonParser.parseString(
            genesisOverridesJson(inferenceConfig.copy(voteExtensionsEnableHeight = 2))
        ).asJsonObject
        assertThat(json.has("app_state")).isTrue()
        val abci = json.getAsJsonObject("consensus").getAsJsonObject("params").getAsJsonObject("abci")
        // int64 must be a JSON string, as CometBFT genesis encodes it.
        assertThat(abci.get("vote_extensions_enable_height").asJsonPrimitive.isString).isTrue()
        assertThat(abci.get("vote_extensions_enable_height").asString).isEqualTo("2")
    }

    @Test
    fun `consensus only override is valid json`() {
        val json = JsonParser.parseString(
            genesisOverridesJson(inferenceConfig.copy(genesisSpec = null, voteExtensionsEnableHeight = 5))
        ).asJsonObject
        assertThat(json.has("app_state")).isFalse()
        assertThat(json.has("consensus")).isTrue()
    }

    @Test
    fun `negative height rejected`() {
        assertThatThrownBy { genesisOverridesJson(inferenceConfig.copy(voteExtensionsEnableHeight = -1)) }
            .isInstanceOf(IllegalArgumentException::class.java)
    }

    @Test
    fun `genesis enable height parses from string`() {
        val g = genesisWith(""","consensus":{"params":{"abci":{"vote_extensions_enable_height":"2"}}}""")
        assertThat(g.consensus?.params?.abci?.voteExtensionsEnableHeight).isEqualTo(2L)
    }

    @Test
    fun `legacy consensus_params layout is read too`() {
        val g = genesisWith(""","consensus_params":{"abci":{"vote_extensions_enable_height":"2"}}""")
        assertThat(g.voteExtensionsEnableHeight).isEqualTo(2L)
        assertThat(voteExtensionsHeightMatches(inferenceConfig.copy(voteExtensionsEnableHeight = 2), g)).isTrue()
    }

    @Test
    fun `cluster match treats missing and null as disabled`() {
        val disabled = genesisWith(""","consensus":{"params":{"abci":{"vote_extensions_enable_height":"0"}}}""")
        val missing = genesisWith("")
        val enabled = genesisWith(""","consensus":{"params":{"abci":{"vote_extensions_enable_height":"2"}}}""")
        val off = inferenceConfig.copy(voteExtensionsEnableHeight = null)
        val on = inferenceConfig.copy(voteExtensionsEnableHeight = 2)

        assertThat(voteExtensionsHeightMatches(off, disabled)).isTrue()
        assertThat(voteExtensionsHeightMatches(off, missing)).isTrue()
        assertThat(voteExtensionsHeightMatches(off, enabled)).isFalse()
        assertThat(voteExtensionsHeightMatches(on, enabled)).isTrue()
        assertThat(voteExtensionsHeightMatches(on, disabled)).isFalse()
    }
}
