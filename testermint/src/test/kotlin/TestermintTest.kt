import com.productscience.TestFilesWriter
import com.productscience.logContext
import com.productscience.logSection
import org.assertj.core.api.Assertions
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.TestInfo
import org.junit.jupiter.api.TestInstance
import org.junit.jupiter.api.extension.ExtendWith
import org.junit.jupiter.api.extension.ExtensionContext
import org.junit.jupiter.api.extension.TestWatcher
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@ExtendWith(LogTestWatcher::class)
open class TestermintTest {
    @BeforeEach
    fun beforeEach(testInfo: TestInfo) {
        val displayName = testInfo.testClass.get().simpleName + "-" + testInfo.displayName.trimEnd('(', ')')
        ThreadContext.put("test", displayName)
        ThreadContext.put("pair", "none")
        ThreadContext.put("source", "test")
        ThreadContext.put("operation", "base")
        TestFilesWriter.currentTest = displayName
        logSection("Test started: $displayName")
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
                        "source" to "test"
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
        val displayName = context.testClass.get().simpleName + "-" + context.displayName.trimEnd('(', ')')
        logSection("Test passed: $displayName")
        TestFilesWriter.currentTest = null
        ThreadContext.remove("test")
        super.testSuccessful(context)
    }

    override fun testFailed(context: ExtensionContext, cause: Throwable) {
        val displayName = context.testClass.get().simpleName + "-" + context.displayName.trimEnd('(', ')')
        logSection("Test failed: $displayName")
        Logger.error(cause, "Test failed:{}", displayName)
        TestFilesWriter.currentTest = null
        ThreadContext.remove("test")
        super.testFailed(context, cause)
    }
}
