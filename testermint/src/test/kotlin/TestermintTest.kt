import com.productscience.TestFilesWriter
import com.productscience.logContext
import org.assertj.core.api.Assertions
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.TestInfo
import org.junit.jupiter.api.TestInstance
import org.junit.jupiter.api.extension.ExtendWith
import org.junit.jupiter.api.extension.ExtensionContext
import org.junit.jupiter.api.extension.RegisterExtension
import org.junit.jupiter.api.extension.TestWatcher
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.util.Optional

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@ExtendWith(LogTestWatcher::class)
open class TestermintTest {
    @BeforeEach
    fun beforeEach(testInfo: TestInfo) {
        val displayName = testInfo.testClass.get().simpleName + "-" + testInfo.displayName.trimEnd('(', ')')
        ThreadContext.put("test", displayName)
        TestFilesWriter.currentTest = displayName
        Logger.warn("Starting test:{}", displayName)
    }

    companion object {
        @JvmStatic
        @BeforeAll
        fun initLogging(): Unit {
            if (loggingStarted) {
                return
            }
            Assertions.setDescriptionConsumer {
                logContext(
                    mapOf(
                        "operation" to "assertion",
                        "source" to "testermint"
                    )
                ) {
                    Logger.info("Test assertion={}", it)
                }
            }
            loggingStarted = true
        }
    }

}

var loggingStarted = false

class LogTestWatcher : TestWatcher {
    override fun testSuccessful(context: ExtensionContext) {
        Logger.warn("Test successful:{}", context.displayName)
        TestFilesWriter.currentTest = null
        ThreadContext.remove("test")
        super.testSuccessful(context)
    }

    override fun testFailed(context: ExtensionContext, cause: Throwable) {
        Logger.error(cause, "Test failed:{}", context.displayName)
        TestFilesWriter.currentTest = null
        ThreadContext.remove("test")
        super.testFailed(context, cause)
    }
}
