package com.productscience.resources

import io.modelcontextprotocol.kotlin.sdk.ReadResourceResult
import io.modelcontextprotocol.kotlin.sdk.Resource
import io.modelcontextprotocol.kotlin.sdk.TextResourceContents
import io.modelcontextprotocol.kotlin.sdk.server.RegisteredResource
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * Data class representing a SQL query
 */
@Serializable
data class SqlQuery(
    val id: String,
    val name: String,
    val description: String,
    val query: String
)

fun getSqlQueryResources(): List<RegisteredResource> {
    val sqlQueries = loadSqlQueries("sql_queries/baseline_queries.json")

    return sqlQueries.map { query ->
        RegisteredResource(
            Resource(
                "sql:${query.id}",
                "SQL:" + query.name,
                query.description,
                "application/sql"
            )
        ) {
            ReadResourceResult(
                contents = listOf(
                    TextResourceContents(
                        query.query,
                        "sql:${query.id}",
                        "application/sql"
                    )
                )
            )
        }
    }
}


/**
 * Data class representing a collection of SQL queries
 */
@Serializable
data class SqlQueries(
    val queries: List<SqlQuery>
)

/**
 * Load SQL queries from a JSON file in the classpath
 *
 * @param resourcePath Path to the JSON file containing SQL queries, relative to classpath
 * @return List of SqlQuery objects
 */
fun loadSqlQueries(resourcePath: String): List<SqlQuery> {
    val resourceStream = object {}.javaClass.classLoader.getResourceAsStream(resourcePath)

    if (resourceStream == null) {
        println("SQL queries resource not found: $resourcePath")
        return emptyList()
    }

    return try {
        val jsonContent = resourceStream.bufferedReader().use { it.readText() }
        val queries = Json.decodeFromString<SqlQueries>(jsonContent)
        queries.queries
    } catch (e: Exception) {
        println("Error loading SQL queries: ${e.message}")
        emptyList()
    }
}
