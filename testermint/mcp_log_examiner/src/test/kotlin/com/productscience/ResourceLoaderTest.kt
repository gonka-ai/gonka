package com.productscience

import com.productscience.resources.loadGuides
import com.productscience.resources.loadSqlQueries
import com.productscience.resources.parseGuideFromMarkdown
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ResourceLoaderTest {

    @Test
    fun `should load SQL queries from classpath`() {
        // Given a JSON file with SQL queries in the classpath
        val queriesPath = "sql_queries/baseline_queries.json"

        // When loading the queries
        val queries = loadSqlQueries(queriesPath)

        // Then the queries should be loaded correctly
        assertThat(queries).isNotEmpty
        assertThat(queries.size).isEqualTo(4)

        // Verify first query - "first errors"
        val firstErrorsQuery = queries.find { it.id == "first_errors" }
        assertThat(firstErrorsQuery).isNotNull
        assertThat(firstErrorsQuery?.name).isEqualTo("first errors")
        assertThat(firstErrorsQuery?.description).contains("Shows all Errors and TestSections")
        assertThat(firstErrorsQuery?.query).contains("level = 'ERROR' OR message LIKE 'TestSection:%'")

        // Verify second query - "warnings and errors"
        val warningsAndErrorsQuery = queries.find { it.id == "warnings_and_errors" }
        assertThat(warningsAndErrorsQuery).isNotNull
        assertThat(warningsAndErrorsQuery?.name).isEqualTo("warnings and errors")
        assertThat(warningsAndErrorsQuery?.description).contains("Shows all Errors, Warnings, and TestSections")
        assertThat(warningsAndErrorsQuery?.query).contains("level IN ('ERROR', 'WARN') OR message LIKE 'TestSection:%'")

        // Verify third query - "genesis only"
        val genesisOnlyQuery = queries.find { it.id == "genesis_only" }
        assertThat(genesisOnlyQuery).isNotNull
        assertThat(genesisOnlyQuery?.name).isEqualTo("genesis only")
        assertThat(genesisOnlyQuery?.description).contains("Limits the results to logs from the genesis pair only")
        assertThat(genesisOnlyQuery?.query).contains("pair = 'genesis'")

        // Verify fourth query - "subsystem filter"
        val subsystemFilterQuery = queries.find { it.id == "subsystem_filter" }
        assertThat(subsystemFilterQuery).isNotNull
        assertThat(subsystemFilterQuery?.name).isEqualTo("subsystem filter")
        assertThat(subsystemFilterQuery?.description).contains("Filter logs by a specific subsystem")
        assertThat(subsystemFilterQuery?.query).contains("subsystem = :subsystem")
    }

    @Test
    fun `should load guides from classpath`() {
        // Given a directory with markdown files in the classpath
        val guidesPath = "guides"

        // When loading the guides
        val guides = loadGuides(guidesPath)

        // Then the guides should be loaded correctly
        assertThat(guides).isNotEmpty

        // Verify guide properties
        val guide = guides.first()
        assertThat(guide.id).isEqualTo("logging_overview")
        assertThat(guide.name).isEqualTo("Logging Overview")
        assertThat(guide.description).isEqualTo("An overview of the structure of the logs")
        assertThat(guide.content).contains("# Logging Overview")
    }

    @Test
    fun `should parse guide name and description from markdown`() {
        // Given a markdown content with a headline and description
        val mdContent = """
            # Test Guide Title

            This is the description of the guide.
            It can span multiple lines.

            ## Section 1

            Other content here.
        """.trimIndent()

        // Create an input stream from the content
        val inputStream = mdContent.byteInputStream()

        // When parsing the guide
        val guide = parseGuideFromMarkdown(inputStream, "test_guide.md")

        // Then the name and description should be extracted correctly
        assertThat(guide.id).isEqualTo("test_guide")
        assertThat(guide.name).isEqualTo("Test Guide Title")
        assertThat(guide.description).isEqualTo("This is the description of the guide.\nIt can span multiple lines.")
    }

    @Test
    fun `should handle missing resources gracefully`() {
        // Given a non-existent resource path
        val nonExistentPath = "non_existent_directory/non_existent_file.json"

        // When trying to load the resource
        val queries = loadSqlQueries(nonExistentPath)

        // Then an empty list should be returned
        assertThat(queries).isEmpty()

        // Given a non-existent directory
        val nonExistentDir = "non_existent_directory"

        // When trying to load guides from the directory
        val guides = loadGuides(nonExistentDir)

        // Then an empty list should be returned
        assertThat(guides).isEmpty()
    }
}
