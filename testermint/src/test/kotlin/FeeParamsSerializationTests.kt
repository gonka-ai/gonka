import com.google.gson.JsonParser
import com.productscience.cosmosJson
import com.productscience.data.FeeParamsData
import com.productscience.gsonCamelCase
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test

@Tag("exclude")
class FeeParamsSerializationTests {
    @Test
    fun `Amino fee rule oneofs serialize as protobuf JSON fields`() {
        val aminoJson =
            """
            {
              "enabled_fee_groups": ["epoch"],
              "groups": [{
                "name": "epoch",
                "msgs": [
                  {
                    "type_url": "/inference.inference.MsgPoCV2StoreCommit",
                    "Func": {
                      "type": "inference/MsgGasRule/StoredDelta",
                      "value": {"stored_delta": {"gas_per_unit": "100"}}
                    }
                  },
                  {
                    "type_url": "/inference.inference.MsgSubmitHardwareDiff",
                    "Func": {
                      "type": "inference/MsgGasRule/StoredBytes",
                      "value": {"stored_bytes": {"gas_per_unit": "100", "unit": "kb"}}
                    }
                  },
                  {
                    "type_url": "/example.MsgBatch",
                    "func": {
                      "type": "inference/MsgGasRule/RepeatedLen",
                      "value": {"repeated_len": {"gas_per_unit": "50", "field": "items"}}
                    }
                  }
                ]
              }]
            }
            """.trimIndent()

        val feeParams = cosmosJson.fromJson(aminoJson, FeeParamsData::class.java)
        val serialized = JsonParser.parseString(gsonCamelCase.toJson(feeParams)).asJsonObject
        val rules = serialized.getAsJsonArray("groups")[0]
            .asJsonObject.getAsJsonArray("msgs")

        assertThat(rules[0].asJsonObject.has("Func")).isFalse()
        assertThat(rules[0].asJsonObject.getAsJsonObject("stored_delta").get("gas_per_unit").asString)
            .isEqualTo("100")
        assertThat(rules[1].asJsonObject.has("Func")).isFalse()
        assertThat(rules[1].asJsonObject.getAsJsonObject("stored_bytes").get("unit").asString)
            .isEqualTo("kb")
        assertThat(rules[2].asJsonObject.has("func")).isFalse()
        assertThat(rules[2].asJsonObject.getAsJsonObject("repeated_len").get("field").asString)
            .isEqualTo("items")

        // Serialization must not mutate the queried params retained by the test.
        assertThat(feeParams.groups!![0].asJsonObject.getAsJsonArray("msgs")[0].asJsonObject.has("Func"))
            .isTrue()
    }
}
