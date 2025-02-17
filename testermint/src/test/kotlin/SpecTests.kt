import com.productscience.data.spec
import com.productscience.gsonSnakeCase
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
        val json = spec.toJson(gsonSnakeCase)
        assertThat(json).isEqualTo("""{"camel_cased_value":"test"}""".trimMargin())
    }

    @Test
    fun `output actual app_state`() {
        val spec = inferenceConfig.genesisSpec
        val json = spec?.toJson(gsonSnakeCase)
        println(json)
    }
}


data class Person(val name: String, val age: Int, val gender: String, val camelCasedValue: String)
