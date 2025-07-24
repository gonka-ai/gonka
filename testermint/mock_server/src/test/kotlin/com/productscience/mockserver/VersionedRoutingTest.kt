package com.productscience.mockserver

import io.ktor.client.request.*
import io.ktor.client.statement.*
import io.ktor.http.*
import io.ktor.server.testing.*
import kotlin.test.*
import org.assertj.core.api.Assertions.assertThat

class VersionedRoutingTest {

    @Test
    fun testInferenceRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // Test non-versioned endpoint
        val response1 = client.post("/v1/chat/completions") {
            contentType(ContentType.Application.Json)
            setBody("""{"model": "test", "messages": []}""")
        }
        
        // Test versioned endpoint  
        val response2 = client.post("/v3.0.8/v1/chat/completions") {
            contentType(ContentType.Application.Json)
            setBody("""{"model": "test", "messages": []}""")
        }
        
        // Both should work (return 200 OK)
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }

    @Test
    fun testPowRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // Test non-versioned endpoint
        val response1 = client.get("/api/v1/pow/status")
        
        // Test versioned endpoint
        val response2 = client.get("/v3.0.8/api/v1/pow/status")
        
        // Both should work
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }

    @Test
    fun testStateRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // Test non-versioned endpoint
        val response1 = client.get("/api/v1/state")
        
        // Test versioned endpoint
        val response2 = client.get("/v3.0.8/api/v1/state")
        
        // Both should work
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }

    @Test
    fun testHealthRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // First ensure we're in STOPPED state, then set to INFERENCE so health checks pass
        client.post("/api/v1/stop")
        client.post("/api/v1/inference/up")
        
        // Test non-versioned endpoint
        val response1 = client.get("/health")
        
        // Test versioned endpoint
        val response2 = client.get("/v3.0.8/health")
        
        // Both should work
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }

    @Test
    fun testTokenizationRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // Test non-versioned endpoint
        val response1 = client.post("/tokenize") {
            contentType(ContentType.Application.Json)
            setBody("""{"model": "test", "prompt": "Hello"}""")
        }
        
        // Test versioned endpoint
        val response2 = client.post("/v3.0.8/tokenize") {
            contentType(ContentType.Application.Json)
            setBody("""{"model": "test", "prompt": "Hello"}""")
        }
        
        // Both should work
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }

    @Test
    fun testStopRoutesVersioning() = testApplication {
        application {
            module()
        }
        
        // Test non-versioned endpoint
        val response1 = client.post("/api/v1/stop")
        
        // Test versioned endpoint
        val response2 = client.post("/v3.0.8/api/v1/stop")
        
        // Both should work
        assertEquals(HttpStatusCode.OK, response1.status)
        assertEquals(HttpStatusCode.OK, response2.status)
    }
} 