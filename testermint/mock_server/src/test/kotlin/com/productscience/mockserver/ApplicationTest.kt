package com.productscience.mockserver

import io.ktor.client.request.*
import io.ktor.client.statement.*
import io.ktor.http.*
import io.ktor.server.testing.*
import kotlin.test.*
import org.assertj.core.api.Assertions.assertThat

class ApplicationTest {
    @Test
    fun testStatusEndpoint() = testApplication {
        application {
            module()
        }
        
        val response = client.get("/status")
        
        assertEquals(HttpStatusCode.OK, response.status)
        val responseText = response.bodyAsText()
        
        // Verify the response contains expected fields
        assertThat(responseText).contains("status")
        assertThat(responseText).contains("ok")
        assertThat(responseText).contains("version")
        assertThat(responseText).contains("timestamp")
    }
}