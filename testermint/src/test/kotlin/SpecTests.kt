import com.productscience.data.TopMinersResponse
import com.productscience.data.spec
import com.productscience.cosmosJson
import com.productscience.inferenceConfig
import org.assertj.core.api.Assertions.assertThat

import org.junit.jupiter.api.Test

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
}

data class Nested(val group:String, val person:Person)

data class Person(val name: String, val age: Int, val gender: String, val camelCasedValue: String)


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
