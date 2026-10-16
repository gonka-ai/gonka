import com.productscience.PromptHashing
import com.google.gson.Gson
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class PromptHashingTests {
    private val gson = Gson()

    @Test
    fun `modified prompt hash matches cross-language vector`() {
        val requestJson = """{"model":"Qwen/Qwen2.5-7B-Instruct","temperature":0.8,"messages":[{"role":"system","content":"Regardless of the language of the question, answer in english"},{"role":"user","content":"When did Hawaii become a state"}]}"""

        val payloadHash = PromptHashing.computeModifiedPromptPayloadAndHash(requestJson, 0)
        val hash = payloadHash.promptHash
        println("KOTLIN_UNIT_CANONICAL_JSON_RAW_BEGIN")
        print(payloadHash.canonicalPayload)
        println("KOTLIN_UNIT_CANONICAL_JSON_RAW_END")
        println("KOTLIN_UNIT_CANONICAL_JSON_QUOTED=${gson.toJson(payloadHash.canonicalPayload)}")
        println("KOTLIN_UNIT_PROMPT_HASH=$hash")

        // NOTE: keep this hash in sync with TestModifyRequestBodyWithLogprobsMode_KotlinCrossLanguageVector
        // in common/completionapi/request_test.go -- same input, same expected hash.
        val expectedHash = "8956540596acd0ff60a29ed0d510e70e81934a7fe6dbf833b1e90767971af3f7"
        assertThat(hash).isEqualTo(expectedHash)
    }

    @Test
    fun `modify request body matches Go mutation semantics`() {
        val requestJson = """{"model":"Qwen/Qwen2.5-7B-Instruct","stream":true,"stream_options":"invalid","messages":[{"role":"user","content":"Hi"}]}"""
        val modifiedJson = PromptHashing.modifyRequestBody(requestJson, 7)
        @Suppress("UNCHECKED_CAST")
        val modifiedMap = gson.fromJson(modifiedJson, Map::class.java) as Map<String, Any?>

        assertThat(modifiedMap["logprobs"]).isEqualTo(true)
        assertThat(modifiedMap["top_logprobs"]).isEqualTo(5.0)
        assertThat(modifiedMap["max_tokens"]).isEqualTo(5000.0)
        assertThat(modifiedMap["max_completion_tokens"]).isEqualTo(5000.0)
        assertThat(modifiedMap).doesNotContainKey("min_tokens")
        assertThat(modifiedMap["skip_special_tokens"]).isEqualTo(false)
        assertThat(modifiedMap["seed"]).isEqualTo(7.0)
        assertThat(modifiedMap["logprobs_mode"]).isEqualTo("processed_logprobs")
        @Suppress("UNCHECKED_CAST")
        val streamOptions = modifiedMap["stream_options"] as Map<String, Any?>
        assertThat(streamOptions["include_usage"]).isEqualTo(true)
    }

    @Test
    fun `grammar-constrained prompt hashes match cross-language vectors`() {
        // Keep in sync with TestModifyRequestBodyWithLogprobsMode_KotlinGrammarVectors in
        // common/completionapi/request_test.go -- same inputs, same expected hashes.
        val vectors = listOf(
            Triple(""""response_format":{"type":"json_object"}""", true, "14375a569703654ee49597e76e438a9bc4a4077af752f8d6372d63be23b58f3f"),
            Triple(""""response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"}}}""", true, "51118e2c54ff99d1d62b191728d77c5d9582e940d86f666bfe2ba9c852d84273"),
            Triple(""""response_format":{"type":"structural_tag","structures":[],"triggers":[]}""", true, "6ce640f00a6c178c33cf745b51c1a50461aac03262b0a68dd184dc7253631755"),
            Triple(""""structured_outputs":{"regex":"[a-z]+"}""", true, "6a6583c4d8b4d7b7c86f74376e39dd0a68899f697b8a97c73b59a6685653723e"),
            Triple(""""structured_outputs":{"choice":["yes","no"]}""", true, "82d7ac91c6881194fb8024104eee99cee2a7c42ee9bf93da6117108ca3444920"),
            Triple(""""structured_outputs":{"json_object":true}""", true, "8631718544e8fc5058fc88c48c8eb7c08fdfeb3b987f293db03f47d7a70d0f66"),
            Triple(""""tool_choice":"required"""", true, "4b256311efaf6dc9a557d43c5539db0b4f654009c940f911f9ca1cb765eccd8f"),
            Triple(""""tool_choice":{"type":"function","function":{"name":"get_weather"}}""", true, "fa5706891721b4ad0245ee0e061089c5b30edc55116f62636d185183268c24a3"),
            Triple(""""response_format":{"type":"text"}""", false, "4757432586a04dc9d98a226bf4f5932f6dfbec2ab73240ec945f5a0d6b94d907"),
            Triple(""""structured_outputs":{"json_object":false}""", false, "57508112a67145d91c0300de0b69d1a34a821c6a6f654c0983528e5d47ebf89e"),
            Triple(""""tool_choice":"auto"""", false, "a8094f7e1cf2de859a9ab0e505d89fbf99e215276690b98ef14c8306bf97b22a"),
            Triple(""""tool_choice":{"type":"function","function":{"name":""}}""", false, "b1e4fb885dfa07cc0cd83e98c1fe64d7e4f372ac12ad979a5ce148297597ced8"),
        )
        for ((field, grammar, expectedHash) in vectors) {
            val requestJson = """{"model":"m","max_tokens":100,"min_tokens":16,"messages":[{"role":"user","content":"hi"}],$field}"""
            @Suppress("UNCHECKED_CAST")
            val modifiedMap = gson.fromJson(PromptHashing.modifyRequestBody(requestJson, 0), Map::class.java) as Map<String, Any?>
            if (grammar) {
                assertThat(modifiedMap).describedAs(field).doesNotContainKey("min_tokens")
            } else {
                assertThat(modifiedMap["min_tokens"]).describedAs(field).isEqualTo(16.0)
            }
            assertThat(PromptHashing.computeModifiedPromptHash(requestJson, 0)).describedAs(field).isEqualTo(expectedHash)
        }
    }

    @Test
    fun `logprob manipulation produces different hash`() {
        // Security test: payloads with same content but different logprobs must have different hashes
        // This prevents attack where executor serves fake logprobs with valid content
        val payloadWithRealLogprobs = """{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.5,"top_logprobs":[{"token":"Hello","logprob":-0.5}]}]}}]}"""
        val payloadWithFakeLogprobs = """{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.1,"top_logprobs":[{"token":"Hello","logprob":-0.1}]}]}}]}"""

        val hash1 = PromptHashing.sha256Hex(payloadWithRealLogprobs)
        val hash2 = PromptHashing.sha256Hex(payloadWithFakeLogprobs)

        assertThat(hash1).isNotEqualTo(hash2)
    }
}
