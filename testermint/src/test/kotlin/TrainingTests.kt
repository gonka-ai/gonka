import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.setupLocalCluster
import org.junit.jupiter.api.Test

class TrainingTests : TestermintTest() {
    @Test
    fun test() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        
    }
}
