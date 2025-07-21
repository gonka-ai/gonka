plugins {
    kotlin("jvm") version "2.0.10"
}

group = "com.productscience"
version = "1.0-SNAPSHOT"

repositories {
    mavenCentral()
}

/**
 * Custom Gradle tasks for dynamically scanning test classes
 * 
 * These tasks are used by the GitHub Actions workflow to dynamically generate
 * the test matrix instead of using a hardcoded list of test classes.
 * 
 * The tasks output the test class names in a format that can be directly used
 * by GitHub Actions to create a matrix for parallel test execution.
 */

/**
 * Lists all test classes in the project
 * 
 * This task scans the test directory for all files ending with "Tests.kt" or "Test.kt"
 * and outputs their names (without the .kt extension) as a comma-separated list of quoted strings.
 * 
 * Used by the "run-tests" command in the GitHub Actions workflow.
 * Note: The actual filtering of tests with "unstable" or "exclude" tags happens at runtime
 * when the tests are executed, not during this scanning phase.
 */
tasks.register("listAllTestClasses") {
    doLast {
        val testClassesDir = file("${projectDir}/src/test/kotlin")
        val testClasses = testClassesDir.listFiles()
            ?.filter { it.isFile && (it.name.endsWith("Tests.kt") || it.name.endsWith("Test.kt")) }
            ?.map { it.nameWithoutExtension }
            ?.sorted()
            ?: emptyList()
            
        // Output in JSON format for GitHub Actions
        val jsonOutput = testClasses.joinToString(",") { "\"$it\"" }
        println(jsonOutput)
    }
}

/**
 * Lists only test classes that contain tests with the "sanity" tag
 * 
 * This task scans the test directory for files that contain the "@Tag("sanity")" annotation
 * and outputs the names of those classes as a comma-separated list of quoted strings.
 * 
 * Used by the "run-sanity" command in the GitHub Actions workflow.
 * This pre-filters the classes during scanning, so only classes with sanity tests are included
 * in the matrix. The actual inclusion of only sanity-tagged tests happens at runtime.
 */
tasks.register("findSanityTestClasses") {
    doLast {
        val testClassesDir = file("${projectDir}/src/test/kotlin")
        val sanityTestClasses = mutableSetOf<String>()
        
        testClassesDir.listFiles()
            ?.filter { it.isFile && (it.name.endsWith("Tests.kt") || it.name.endsWith("Test.kt")) }
            ?.forEach { file ->
                val content = file.readText()
                if (content.contains("@Tag(\"sanity\")")) {
                    sanityTestClasses.add(file.nameWithoutExtension)
                }
            }
            
        // Output in JSON format for GitHub Actions
        val jsonOutput = sanityTestClasses.sorted().joinToString(",") { "\"$it\"" }
        println(jsonOutput)
    }
}

dependencies {
    implementation("com.github.docker-java:docker-java:3.4.0")
    implementation("com.github.docker-java:docker-java-transport-httpclient5:3.4.0")
    implementation("com.google.code.gson:gson:2.10")
    implementation("com.github.kittinunf.fuel:fuel:2.3.1")
    implementation("com.github.kittinunf.fuel:fuel-gson:2.3.1")  // For Gson support
    implementation("org.tinylog:tinylog-api-kotlin:2.8.0-M1")
    implementation("org.tinylog:tinylog-impl:2.8.0-M1")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.7.3")
    implementation("org.jetbrains.kotlin:kotlin-reflect:2.0.10")
    implementation("org.reflections:reflections:0.10.2")
    implementation("org.apache.tuweni:tuweni-crypto:2.3.0")
    implementation("org.bouncycastle:bcprov-jdk15to18:1.78") // or latest
    implementation("org.bitcoinj:bitcoinj-core:0.16.2") // or latest version

// Kubernetes Java client
    implementation("io.kubernetes:client-java:18.0.1")
    testImplementation(kotlin("test"))
    // Add AssertJ for fluent assertions
    testImplementation("org.assertj:assertj-core:3.26.3")
    implementation("org.wiremock:wiremock:3.10.0")
}

tasks.test {
    outputs.upToDateWhen { false }
    useJUnitPlatform {
        val includeTags = System.getProperty("includeTags")
        val excludeTags = System.getProperty("excludeTags")
        if (includeTags != null) {
            includeTags(*includeTags.split(",").toTypedArray())
        }
        if (excludeTags != null) {
            excludeTags(*excludeTags.split(",").toTypedArray())
        }
    }

}
kotlin {
    jvmToolchain(19)
}
