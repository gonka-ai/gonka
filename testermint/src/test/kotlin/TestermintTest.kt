import com.productscience.logContext
import org.assertj.core.api.Assertions
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.TestInfo
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger

open class TestermintTest {
    @BeforeEach
    fun beforeEach(testInfo: TestInfo) {
        ThreadContext.put("test", testInfo.displayName)
        Logger.warn("Starting test:" + testInfo.displayName)
    }

    @AfterEach
    fun afterEach(testInfo: TestInfo) {
        Logger.warn("Finished test:" + testInfo.displayName)
        ThreadContext.remove("test")
    }

    companion object {
        @JvmStatic
        @BeforeAll
        fun initLogging(): Unit {
            Assertions.setDescriptionConsumer {
                logContext(
                    mapOf(
                        "operation" to "assertion",
                        "source" to "testermint"
                    )
                ) {
                    Logger.info("Asserting: $it")
                }
            }
        }
    }

}
