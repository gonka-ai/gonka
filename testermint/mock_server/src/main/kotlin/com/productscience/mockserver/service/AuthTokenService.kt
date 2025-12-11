package com.productscience.mockserver.service

import org.slf4j.LoggerFactory
import java.util.concurrent.ConcurrentHashMap
import com.productscience.mockserver.service.HostName

class AuthTokenService {
    private val logger = LoggerFactory.getLogger(AuthTokenService::class.java)

    private val expectedHeaders = ConcurrentHashMap<HostName, String>()

    fun setExpectedHeader(host: HostName?, header: String) {
        val key = host ?: HostName("localhost")
        expectedHeaders[key] = header
        logger.info("Auth expected header set for host {}", key.name)
    }

    fun clearExpectedHeader(host: HostName?) {
        val key = host ?: HostName("localhost")
        expectedHeaders.remove(key)
        logger.info("Auth expected header cleared for host {}", key.name)
    }

    fun getExpectedHeader(host: HostName): String? = expectedHeaders[host]
}