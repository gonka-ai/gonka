import com.productscience.data.TopMinersResponse
import com.productscience.data.spec
import com.productscience.cosmosJson
import com.productscience.data.Coin
import com.productscience.data.CreatePartialUpgrade
import com.productscience.data.Decimal
import com.productscience.data.camelToSnake
import com.productscience.gsonCamelCase
import com.productscience.inferenceConfig
import com.productscience.openAiJson
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import java.time.Duration

@Tag("exclude")
class DecimalTests {
    @Test
    fun `test decimal toDouble conversion`() {
        val decimal = Decimal(1234, -2)
        assertThat(decimal.toDouble()).isEqualTo(12.34)
    }

    @Test
    fun `test decimal fromFloat whole number`() {
        val decimal = Decimal.fromFloat(12f)
        assertThat(decimal.value).isEqualTo(12)
        assertThat(decimal.exponent).isEqualTo(0)
    }

    @Test
    fun `test decimal fromFloat with one decimal place`() {
        val decimal = Decimal.fromFloat(12.5f)
        assertThat(decimal.value).isEqualTo(125)
        assertThat(decimal.exponent).isEqualTo(-1)
    }

    @Test
    fun `test decimal fromFloat with multiple decimal places`() {
        val decimal = Decimal.fromFloat(12.345f)
        assertThat(decimal.value).isEqualTo(12345)
        assertThat(decimal.exponent).isEqualTo(-3)
    }
}

@Tag("exclude")
class TxMessageSerializationTests {
    @Test
    fun `simple message`() {
        val message = CreatePartialUpgrade("creator", "50", "v1", "")
        println(gsonCamelCase.toJson(message))
    }

    @Test
    fun `duration`() {
        val duration = Duration.parse("PT48h0m0s")
        assertThat(duration.toDays()).isEqualTo(2)
    }
}


@Tag("exclude")
class SpecTests {
    @Test
    fun `test simple spec`() {
        val actual = Person("John", 25, "male", camelCasedValue = "test")
        val failingSpec = spec<Person>{
            this[Person::age] = 10
            this[Person::name] = "John"
        }
        val passingSpec = spec<Person>{
            this[Person::age] = 25
            this[Person::name] = "John"
        }
        assertThat(failingSpec.matches(actual)).isFalse()
        assertThat(passingSpec.matches(actual)).isTrue()
    }

    @Test
    fun `output spec to json`() {
        val spec = spec<Person>{
            this[Person::age] = 10
            this[Person::name] = "John"
        }
        val json = spec.toJson()
        assertThat(json).isEqualTo("""{
            |  "age": 10,
            |  "name": "John"
            |}""".trimMargin())
    }

    @Test
    fun `output spec with snake_case`() {
        val spec = spec<Person>{
            this[Person::camelCasedValue] = "test"
        }
        // Nice, huh? Trickier than it seemed, but totally works
        val json = spec.toJson(cosmosJson)
        assertThat(json).isEqualTo("""{"camel_cased_value":"test"}""".trimMargin())
    }

    @Test
    fun `output actual app_state`() {
        val spec = inferenceConfig.genesisSpec
        val json = spec?.toJson(cosmosJson)
        println(json)
    }

    @Test
    fun `merge specs`() {
        val spec1 = spec<Person>{
            this[Person::age] = 10
        }
        val spec2 = spec<Person>{
            this[Person::name] = "John"
        }
        val merged = spec1.merge(spec2)
        println(merged.toJson(cosmosJson))
    }

    @Test
    fun `merge nested`() {
        val spec1 = spec<Nested>{
            this[Nested::person] = spec<Person>{
                this[Person::age] = 10
            }
        }
        val spec2 = spec<Nested>{
            this[Nested::person] = spec<Person>{
                this[Person::name] = "John"
            }
        }
        val merged = spec1.merge(spec2)
        println(merged.toJson(cosmosJson))
    }

    @Test
    fun `parse top miner`() {
        val topMiners = cosmosJson.fromJson(topMinerJson, TopMinersResponse::class.java)
        println(topMiners)
    }

    @Test
    fun `invalid argument if type does not match`() {
        val result = runCatching {
            val spec = spec<Person>{
                this[Person::age] = "test"
            }
        }
        assertThat(result.isFailure).isTrue()
    }

    @Test
    fun `test spec with list of coins`() {
        val coins = listOf(
            Coin("nicoin", 100),
            Coin("bitcoin", 200)
        )

        val spec = spec<WithCoins> {
            this[WithCoins::coins] = coins
        }

        val actual = WithCoins(coins)
        assertThat(spec.matches(actual)).isTrue()

        val differentCoins = listOf(
            Coin("nicoin", 300),
            Coin("bitcoin", 400)
        )
        val different = WithCoins(differentCoins)
        assertThat(spec.matches(different)).isFalse()
    }

    @Test
    fun `test spec with duration`() {
        val duration = Duration.ofMinutes(30)

        val spec = spec<WithDuration> {
            this[WithDuration::duration] = duration
        }

        val actual = WithDuration(duration)
        assertThat(spec.matches(actual)).isTrue()

        val different = WithDuration(Duration.ofMinutes(45))
        assertThat(spec.matches(different)).isFalse()
    }

    @Test
    fun `output spec with list of coins to json`() {
        val coins = listOf(
            Coin("nicoin", 100),
            Coin("bitcoin", 200)
        )

        val spec = spec<WithCoins> {
            this[WithCoins::coins] = coins
        }

        val json = spec.toJson(cosmosJson)
        println(json)
        assertThat(json).contains("\"coins\":")
        assertThat(json).contains("\"denom\":\"nicoin\"")
        assertThat(json).contains("\"amount\":\"100\"")
        assertThat(json).contains("\"denom\":\"bitcoin\"")
        assertThat(json).contains("\"amount\":\"200\"")
    }

    @Test
    fun `output spec with duration to json`() {
        val duration = Duration.ofMinutes(30)

        val spec = spec<WithDuration> {
            this[WithDuration::duration] = duration
        }

        val json = spec.toJson(cosmosJson)
        println(json)
        assertThat(json).contains("\"duration\":\"30m\"")
    }
}

data class Nested(val group:String, val person:Person)

data class Person(val name: String, val age: Int, val gender: String, val camelCasedValue: String)

data class WithCoins(val coins: List<Coin>)

data class WithDuration(val duration: Duration)


val topMinerJson = """
    {
      "top_miner": [
        {
          "address": "cosmos1nrsklffzkzj3lhrmup3vwx9xv8usnz8wqdv0pr",
          "last_qualified_started": "1739651467",
          "last_updated_time": "1739651467",
          "first_qualified_started": "1739651467"
        }
      ],
      "pagination": {
        "total": "1"
      }
    }
""".trimIndent()
