package com.productscience

import java.sql.Connection
import java.sql.DriverManager

/**
 * PostgresClient is a thin JDBC wrapper used by tests that want to assert on
 * the contents of the testermint-postgres container. Tests connect via the
 * host-side mapped port (TESTERMINT_PG_PORT, default 15432) -- the in-cluster
 * dapi/devshardd containers reach the same database via the chain-public
 * docker network as `testermint-postgres:5432`.
 *
 * The class is intentionally not Exposed-based: tests need a handful of small
 * read queries (count rows, check partition existence) and JDBC keeps the
 * dependency surface to one driver jar.
 */
class PostgresClient private constructor(private val conn: Connection) : AutoCloseable {

    /**
     * Returns the number of rows in `table`. Optional `where` is appended
     * verbatim to the query; pass null for no filter. Caller is responsible
     * for the `where` clause being safe -- this helper is for trusted test
     * code, not user input.
     */
    fun countRows(table: String, where: String? = null): Long {
        val sql = if (where == null) "SELECT count(*) FROM $table"
        else "SELECT count(*) FROM $table WHERE $where"
        conn.createStatement().use { st ->
            st.executeQuery(sql).use { rs ->
                check(rs.next()) { "count(*) returned no rows" }
                return rs.getLong(1)
            }
        }
    }

    /**
     * Returns true if `name` resolves to a regular table (or partition) in
     * the current schema search path. Uses to_regclass which returns NULL
     * for missing tables, so this is a single-roundtrip check that does not
     * throw when the table is absent.
     */
    fun tableExists(name: String): Boolean {
        conn.prepareStatement("SELECT to_regclass(?) IS NOT NULL").use { ps ->
            ps.setString(1, name)
            ps.executeQuery().use { rs ->
                check(rs.next()) { "to_regclass returned no rows" }
                return rs.getBoolean(1)
            }
        }
    }

    /**
     * Lists every direct partition of the given parent table, sorted. Useful
     * for assertions like "after pruning epoch 3, devshard_diffs no longer
     * has a devshard_diffs_epoch_3 partition."
     */
    fun partitionsOf(parent: String): List<String> {
        val sql = """
            SELECT c.relname
            FROM pg_class c
            JOIN pg_inherits i ON i.inhrelid = c.oid
            JOIN pg_class p ON p.oid = i.inhparent
            WHERE p.relname = ?
            ORDER BY c.relname
        """.trimIndent()
        conn.prepareStatement(sql).use { ps ->
            ps.setString(1, parent)
            ps.executeQuery().use { rs ->
                val out = mutableListOf<String>()
                while (rs.next()) out += rs.getString(1)
                return out
            }
        }
    }

    /**
     * Returns the first column of the first row of an arbitrary query, or
     * null if the result set is empty. Escape hatch for ad-hoc assertions
     * that don't justify a dedicated helper.
     */
    @Suppress("UNCHECKED_CAST")
    fun <T> firstColumn(sql: String, vararg args: Any?): T? {
        conn.prepareStatement(sql).use { ps ->
            args.forEachIndexed { i, v -> ps.setObject(i + 1, v) }
            ps.executeQuery().use { rs ->
                if (!rs.next()) return null
                return rs.getObject(1) as T?
            }
        }
    }

    override fun close() {
        conn.close()
    }

    companion object {
        /**
         * Open a connection to the testermint-postgres container as exposed
         * to the host. Defaults match docker-compose.postgres.yml; override
         * any of them for a non-standard cluster (e.g. a remote PG for CI).
         */
        fun connect(
            host: String = System.getenv("TESTERMINT_PG_HOST") ?: "127.0.0.1",
            port: Int = (System.getenv("TESTERMINT_PG_PORT") ?: "15432").toInt(),
            database: String = "payloads",
            user: String = "payloads",
            password: String = "test",
        ): PostgresClient {
            // Touch the driver class so it self-registers on JREs that don't
            // auto-load services from META-INF.
            Class.forName("org.postgresql.Driver")
            val url = "jdbc:postgresql://$host:$port/$database"
            val conn = DriverManager.getConnection(url, user, password)
            return PostgresClient(conn)
        }
    }
}
