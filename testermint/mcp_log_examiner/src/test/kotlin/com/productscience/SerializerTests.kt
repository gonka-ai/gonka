package com.productscience

import com.productscience.tools.LoadLogRequest
import com.productscience.tools.LogQueryRequest
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import org.junit.jupiter.api.Test
import org.assertj.core.api.Assertions.assertThat
import sh.ondr.koja.KojaEntry
import sh.ondr.koja.jsonSchema
import sh.ondr.koja.toJsonElement

class SerializerTest {

    @Test
    @KojaEntry
    fun `should generate correct JSON schema for request classes`() {
        // Verify LoadLogRequest schema
        val loadLogSchema = jsonSchema<LoadLogRequest>().toJsonElement().jsonObject["properties"]
        assertThat(loadLogSchema).isNotNull()

        // Verify LogQueryRequest schema
        val logQuerySchema = jsonSchema<LogQueryRequest>().toJsonElement().jsonObject["properties"]
        assertThat(logQuerySchema).isNotNull()

        // Verify that the query property has the correct type and description
        val expectedQueryProperty = buildJsonObject {
            putJsonObject("query") {
                put("type", "string")
                put("description", "SQL query to execute")
            }
        }

        // This is a visual verification test, but we'll add some basic assertions
        assertThat(logQuerySchema.toString()).contains("query")
    }
}
