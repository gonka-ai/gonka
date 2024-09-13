@file:DependsOn("/build/libs/testermint-1.0-SNAPSHOT.jar")

import com.productscience.ApplicationConfig
import com.productscience.getLocalInferencePairs

val inferenceConfig = ApplicationConfig(
    appName = "inferenced",
    chainId = "prod-sim",
    nodeImageName = "inferenced",
    apiImageName = "decentralized-api",
    denom = "icoin",
    stateDirName = ".inference",
)


val pairs = getLocalInferencePairs(inferenceConfig)
println(pairs)

