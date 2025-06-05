package com.productscience.resources

import io.modelcontextprotocol.kotlin.sdk.ReadResourceResult
import io.modelcontextprotocol.kotlin.sdk.Resource
import io.modelcontextprotocol.kotlin.sdk.TextResourceContents
import io.modelcontextprotocol.kotlin.sdk.server.RegisteredResource
import java.io.File

/**
 * Data class representing a guide
 */
data class Guide(
    val id: String,
    val name: String,
    val description: String,
    val content: String
)


fun getGuides(): List<RegisteredResource> {
    return loadGuides("guides").map { guide ->
        val uri = "guide:${guide.id}"
        RegisteredResource(
            Resource(uri, "Guide:" + guide.name, guide.description, "text/markdown")
        ) {
            ReadResourceResult(contents = listOf(TextResourceContents(guide.content, uri, "text/markdown")))
        }
    }
}

/**
 * Load guides from markdown files in a classpath directory
 *
 * @param resourcePath Path to the directory containing markdown files, relative to classpath
 * @return List of Guide objects
 */
fun loadGuides(resourcePath: String): List<Guide> {
    val classLoader = object {}.javaClass.classLoader
    val dirUrl = classLoader.getResource(resourcePath)

    if (dirUrl == null) {
        println("Guides directory not found in classpath: $resourcePath")
        return emptyList()
    }

    return try {
        // For JAR files, we need to use the URI to get the file system
        if (dirUrl.protocol == "jar") {
            val guides = mutableListOf<Guide>()

            // Get all resources from the JAR
            val jarConnection = dirUrl.openConnection() as java.net.JarURLConnection
            val jarFile = jarConnection.jarFile

            // Enumerate all entries in the JAR
            val entries = jarFile.entries()
            val resourcePathWithSlash = if (resourcePath.endsWith("/")) resourcePath else "$resourcePath/"

            while (entries.hasMoreElements()) {
                val entry = entries.nextElement()
                val entryName = entry.name

                // Check if the entry is in our directory and is a markdown file
                if (entryName.startsWith(resourcePathWithSlash) && entryName.endsWith(".md") && entryName != resourcePathWithSlash) {
                    // Get the resource as a stream
                    val resourceStream = classLoader.getResourceAsStream(entryName)
                    if (resourceStream != null) {
                        // Extract filename from the path
                        val fileName = entryName.substringAfterLast('/')
                        guides.add(parseGuideFromMarkdown(resourceStream, fileName))
                    }
                }
            }

            guides
        } else {
            // For file system access during development
            val directory = File(dirUrl.toURI())
            directory.listFiles()
                ?.filter { it.isFile && it.name.endsWith(".md") }
                ?.map { file ->
                    classLoader.getResourceAsStream("$resourcePath/${file.name}")?.let { stream ->
                        parseGuideFromMarkdown(stream, file.name)
                    }
                }
                ?.filterNotNull() ?: emptyList()
        }
    } catch (e: Exception) {
        println("Error loading guides: ${e.message}")
        e.printStackTrace()
        emptyList()
    }
}

/**
 * Parse a guide from a markdown input stream
 *
 * @param inputStream InputStream containing the markdown content
 * @param fileName Name of the markdown file
 * @return Guide object
 */
fun parseGuideFromMarkdown(inputStream: java.io.InputStream, fileName: String): Guide {
    val content = inputStream.bufferedReader().use { it.readText() }
    val lines = content.lines()

    // Extract name from the first headline (# Title)
    val nameRegex = Regex("^# (.+)$")
    val name = lines.firstOrNull { it.matches(nameRegex) }
        ?.replace(nameRegex, "$1")
        ?: fileName.removeSuffix(".md")

    // Extract description (text immediately after the headline)
    val headlineIndex = lines.indexOfFirst { it.matches(nameRegex) }
    val descriptionStartIndex = headlineIndex + 1

    // Find the next headline or end of text
    val nextHeadlineIndex = lines.drop(descriptionStartIndex).indexOfFirst { it.startsWith("#") }
    val descriptionEndIndex = if (nextHeadlineIndex == -1) {
        lines.size
    } else {
        descriptionStartIndex + nextHeadlineIndex
    }

    // Extract description text, skipping empty lines at the beginning
    val descriptionLines = lines.subList(descriptionStartIndex, descriptionEndIndex)
        .dropWhile { it.isBlank() }

    val description = if (descriptionLines.isNotEmpty()) {
        descriptionLines.joinToString("\n").trim()
    } else {
        "No description available"
    }

    // Generate ID from filename
    val id = fileName.removeSuffix(".md").lowercase().replace(Regex("\\s+"), "-")

    return Guide(id, name, description, content)
}
