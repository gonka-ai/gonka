import com.productscience.data.InferenceState
import com.productscience.gsonCamelCase
import org.junit.jupiter.api.Test
import kotlin.reflect.typeOf

class SerializationTests {
    @Test
    fun `serialize inference params`() {
        val inferenceResult = gsonCamelCase.fromJson(inferenceJson, InferenceState::class.java)
        println(inferenceResult)
    }
}



val inferenceJson = """
    {
      "params": {
        "epochParams": {
          "epochLength": "20",
          "epochMultiplier": "1",
          "epochNewCoin": "1048576",
          "coinHalvingInterval": "100"
        },
        "validationParams": {
          "falsePositiveRate": 0.05,
          "minRampUpMeasurements": 10,
          "passValue": 0.99,
          "minValidationAverage": 0.1,
          "maxValidationAverage": 1
        },
        "pocParams": {
          "defaultDifficulty": 5
        }
      },
      "inferenceList": [],
      "participantList": [
        {
          "index": "cosmos18uaw367ytt44vt6jnnvla35dcxzqktkxtk6fj4",
          "address": "cosmos18uaw367ytt44vt6jnnvla35dcxzqktkxtk6fj4",
          "reputation": 0,
          "weight": -1,
          "joinTime": "1738010392454",
          "joinHeight": "2",
          "lastInferenceTime": "0",
          "inferenceUrl": "http://genesis-api:8080",
          "models": [
            "unsloth/llama-3-8b-Instruct"
          ],
          "status": "ACTIVE",
          "promptTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "completionTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "inferenceCount": "0",
          "validatedInferences": "0",
          "invalidatedInferences": "0",
          "coinBalance": "0",
          "validatorKey": "xCAUKm1BmwQsdI140CnxQTsvkyx+7x29jxlaV00q1fU=",
          "refundBalance": "0",
          "consecutiveInvalidInferences": "0"
        },
        {
          "index": "cosmos1k69tnk00p6csn4rrmxz77tcwhnaphz7axqlj40",
          "address": "cosmos1k69tnk00p6csn4rrmxz77tcwhnaphz7axqlj40",
          "reputation": 0,
          "weight": -1,
          "joinTime": "1738010432707",
          "joinHeight": "10",
          "lastInferenceTime": "0",
          "inferenceUrl": "http://join2-api:8080",
          "models": [
            "unsloth/llama-3-8b-Instruct"
          ],
          "status": "ACTIVE",
          "promptTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "completionTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "inferenceCount": "0",
          "validatedInferences": "0",
          "invalidatedInferences": "0",
          "coinBalance": "0",
          "validatorKey": "5OEx2DHV/0PJw80pDKfAO8ciI0AzafXE8Ibjr+oNnQs=",
          "refundBalance": "0",
          "consecutiveInvalidInferences": "0"
        },
        {
          "index": "cosmos1t7s7vh2rjjtvjxjklfgd605sd0jjjpn7nnm455",
          "address": "cosmos1t7s7vh2rjjtvjxjklfgd605sd0jjjpn7nnm455",
          "reputation": 0,
          "weight": -1,
          "joinTime": "1738010432707",
          "joinHeight": "10",
          "lastInferenceTime": "0",
          "inferenceUrl": "http://join1-api:8080",
          "models": [
            "unsloth/llama-3-8b-Instruct"
          ],
          "status": "ACTIVE",
          "promptTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "completionTokenCount": {
            "unsloth/llama-3-8b-Instruct": "0"
          },
          "inferenceCount": "0",
          "validatedInferences": "0",
          "invalidatedInferences": "0",
          "coinBalance": "0",
          "validatorKey": "7jkD+9j/0k+gfTxziNKCi45kxU9Cbr+kTvUATAyapIw=",
          "refundBalance": "0",
          "consecutiveInvalidInferences": "0"
        }
      ],
      "epochGroupDataList": [
        {
          "pocStartBlockHeight": "0",
          "epochGroupId": "0",
          "epochPolicy": "",
          "effectiveBlockHeight": "0",
          "lastBlockHeight": "36",
          "memberSeedSignatures": [],
          "finishedInferences": [],
          "validationWeights": []
        },
        {
          "pocStartBlockHeight": "20",
          "epochGroupId": "1",
          "epochPolicy": "cosmos1afk9zr2hn2jsac63h4hm60vl9z3e5u69gndzf7c99cqge3vzwjzsfwkgpd",
          "effectiveBlockHeight": "37",
          "lastBlockHeight": "0",
          "memberSeedSignatures": [
            {
              "memberAddress": "cosmos18uaw367ytt44vt6jnnvla35dcxzqktkxtk6fj4",
              "signature": "0377a295495cf655793b4a7204e55602030b6cb50dc8d5b659fd32d692772bfd390d3ff96806325d3de90b602a98eeba61f5720ec45f3fc3fcdf91a68789eabc"
            },
            {
              "memberAddress": "cosmos1k69tnk00p6csn4rrmxz77tcwhnaphz7axqlj40",
              "signature": "cf4485f0df414ca974719b5aa32ab64b2f02deb8f17eac9685e276dc8dccc04d595aa6273638bc08b7fff5fa9f4f57888d6491e270fb37e8091853829019c491"
            },
            {
              "memberAddress": "cosmos1t7s7vh2rjjtvjxjklfgd605sd0jjjpn7nnm455",
              "signature": "51d55a6c65c5c7d65fa84e427968f0348e002d82c16ed979a38f81ba015723c30a1f6b770dd2f924713cbf6e073b5dbc305fc6790f6762ddf70d2076bab02289"
            }
          ],
          "finishedInferences": [],
          "validationWeights": [
            {
              "memberAddress": "cosmos18uaw367ytt44vt6jnnvla35dcxzqktkxtk6fj4",
              "weight": "10"
            },
            {
              "memberAddress": "cosmos1k69tnk00p6csn4rrmxz77tcwhnaphz7axqlj40",
              "weight": "10"
            },
            {
              "memberAddress": "cosmos1t7s7vh2rjjtvjxjklfgd605sd0jjjpn7nnm455",
              "weight": "10"
            }
          ]
        },
        {
          "pocStartBlockHeight": "40",
          "epochGroupId": "2",
          "epochPolicy": "cosmos1dlszg2sst9r69my4f84l3mj66zxcf3umcgujys30t84srg95dgvsmn3jeu",
          "effectiveBlockHeight": "0",
          "lastBlockHeight": "0",
          "memberSeedSignatures": [],
          "finishedInferences": [],
          "validationWeights": []
        }
      ],
      "settleAmountList": [],
      "epochGroupValidationsList": []
    }
""".trimIndent()
