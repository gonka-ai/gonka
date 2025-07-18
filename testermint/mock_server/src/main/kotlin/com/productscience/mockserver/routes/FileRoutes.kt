package com.productscience.mockserver.routes

import io.ktor.http.ContentType
import io.ktor.server.application.call
import io.ktor.server.response.respondFile
import io.ktor.server.routing.Route
import io.ktor.server.routing.get
import java.io.File
import org.slf4j.LoggerFactory

private val logger = LoggerFactory.getLogger("FileRoutes")

/**
 * Configures routes for serving files from the public-html directory.
 * Files are served at the path starting with "files/", and the checksum parameter is ignored.
 */
fun Route.fileRoutes() {
    // Route for serving files with "files/" prefix
    get("files/{path...}") {
        val path = call.parameters.getAll("path")?.joinToString("/") ?: ""

        logger.info("Requested file: files/$path")

        // Look for the file in the public-html directory
        val file = File("files/__files/$path")

        if (file.exists() && file.isFile) {
            logger.info("Serving file: ${file.absolutePath}")
            call.respondFile(file)
        } else {
            logger.warn("File not found: ${file.absolutePath}")
            // Let the request continue to other routes
            // This allows the 404 to be handled by Ktor's default handlers
        }
    }
}
