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

        // NOTE: keep this hash in sync with decentralized-api/internal/server/public/post_chat_handler_test.go
        val expectedHash = "4d6342776b9f73058ff9999eb953d795cd5158293d182f87f19ba5fef7706f36"
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
        assertThat(modifiedMap["skip_special_tokens"]).isEqualTo(false)
        assertThat(modifiedMap["seed"]).isEqualTo(7.0)
        @Suppress("UNCHECKED_CAST")
        val streamOptions = modifiedMap["stream_options"] as Map<String, Any?>
        assertThat(streamOptions["include_usage"]).isEqualTo(true)
    }
}
