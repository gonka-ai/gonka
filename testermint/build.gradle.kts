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
 * Lists all test classes in the project that have at least one test that would run with run-tests
 * 
 * This task scans the test directory for all files ending with "Tests.kt" or "Test.kt"
 * and checks if each class:
 * 1. Does NOT have @Tag("unstable") or @Tag("exclude") at the class level
 * 2. Has at least one @Test method that doesn't have @Tag("unstable") or @Tag("exclude")
 * 
 * Only classes that have at least one test that would run after filtering are included.
 * 
 * Used by the "run-tests" command in the GitHub Actions workflow.
 */
tasks.register("listAllTestClasses") {
    doLast {
        val testClassesDir = file("${projectDir}/src/test/kotlin")
        val validTestClasses = mutableListOf<String>()
        
        testClassesDir.listFiles()
            ?.filter { it.isFile && (it.name.endsWith("Tests.kt") || it.name.endsWith("Test.kt")) }
            ?.forEach { file ->
                val content = file.readText()
                
                // Extract all class definitions from the file
                val classDefinitions = extractClassDefinitions(content)
                
                // Process each class in the file separately
                classDefinitions.forEach { (className, classContent) ->
                    // Skip this class if it has exclude or unstable tag at the class level
                    if (hasExcludeOrUnstableTag(classContent, atClassLevel = true)) {
                        return@forEach
                    }
                    
                    // Check if the class has at least one @Test method that doesn't have unstable or exclude tags
                    val testMethods = classContent.split("@Test")
                        .drop(1) // Drop the first part (before the first @Test)
                    
                    // Check if at least one test method doesn't have unstable or exclude tags
                    val hasValidTest = testMethods.any { testMethod ->
                        // Check if the test method doesn't have unstable or exclude tags
                        // We look at the text between @Test and the next function declaration (fun)
                        val testDeclaration = testMethod.substringBefore("fun")
                        !hasExcludeOrUnstableTag(testDeclaration)
                    }
                    
                    if (hasValidTest) {
                        // Use the class name if available, otherwise fall back to file name
                        val testClassName = className ?: file.nameWithoutExtension
                        validTestClasses.add(testClassName)
                    }
                }
            }
            
        // Output in JSON format for GitHub Actions
        val jsonOutput = validTestClasses.sorted().joinToString(",") { "\"$it\"" }
        println(jsonOutput)
    }
}

/**
 * Helper function to extract class definitions from a Kotlin file
 * 
 * This function parses the file content to identify individual class definitions,
 * returning a list of pairs where each pair contains:
 * 1. The class name (or null if it couldn't be determined)
 * 2. The full class content including annotations and methods
 */
fun extractClassDefinitions(fileContent: String): List<Pair<String?, String>> {
    val classRegex = Regex("""(?:@\w+(?:\(.*?\))?[\s\n]*)*class\s+(\w+)""", RegexOption.DOT_MATCHES_ALL)
    val matches = classRegex.findAll(fileContent)
    
    val classes = mutableListOf<Pair<String?, String>>()
    
    matches.forEach { matchResult ->
        val className = matchResult.groupValues[1]
        val startIndex = matchResult.range.first
        
        // Find the class body by counting braces
        var braceCount = 0
        var endIndex = startIndex
        var foundOpenBrace = false
        
        for (i in startIndex until fileContent.length) {
            val char = fileContent[i]
            if (char == '{') {
                foundOpenBrace = true
                braceCount++
            } else if (char == '}') {
                braceCount--
                if (foundOpenBrace && braceCount == 0) {
                    endIndex = i + 1
                    break
                }
            }
        }
        
        // Get everything before the class declaration to capture annotations
        var classStartIndex = startIndex
        // Look for annotations before the class
        for (i in startIndex downTo 0) {
            if (i == 0 || fileContent[i-1] == '\n' && !fileContent[i].isWhitespace() && fileContent[i] != '@') {
                classStartIndex = i
                break
            }
        }
        
        val classContent = fileContent.substring(classStartIndex, endIndex)
        classes.add(Pair(className, classContent))
    }
    
    // If no classes were found, return the whole file content as a single "class"
    if (classes.isEmpty()) {
        classes.add(Pair(null, fileContent))
    }
    
    return classes
}

/**
 * Helper function to check if a string contains exclude or unstable tags
 * 
 * @param content The string to check for tags
 * @param atClassLevel If true, checks for tags at the class level (before the class declaration)
 * @return true if the content has exclude or unstable tags, false otherwise
 */
fun hasExcludeOrUnstableTag(content: String, atClassLevel: Boolean = false): Boolean {
    if (atClassLevel) {
        // For class-level tags, we need to check if the tag appears before the class declaration
        val classIndex = content.indexOf("class ")
        if (classIndex == -1) return false
        
        val beforeClass = content.substring(0, classIndex)
        return beforeClass.contains("@Tag(\"unstable\")") || beforeClass.contains("@Tag(\"exclude\")")
    } else {
        // For method-level tags, just check if they appear anywhere in the content
        return content.contains("@Tag(\"unstable\")") || content.contains("@Tag(\"exclude\")")
    }
}

/**
 * Helper function to check if a string contains a sanity tag
 * 
 * @param content The string to check for the sanity tag
 * @return true if the content has a sanity tag, false otherwise
 */
fun hasSanityTag(content: String): Boolean {
    return content.contains("@Tag(\"sanity\")")
}

/**
 * Lists only test classes that contain tests with the "sanity" tag that would actually run
 * 
 * This task scans the test directory for files and checks if each class:
 * 1. Does NOT have @Tag("unstable") or @Tag("exclude") at the class level
 * 2. Has at least one @Test method with the @Tag("sanity") annotation
 *    that doesn't also have @Tag("unstable") or @Tag("exclude")
 * 
 * Only classes that have at least one sanity-tagged test that would run after filtering are included.
 * 
 * Used by the "run-sanity" command in the GitHub Actions workflow.
 */
tasks.register("findSanityTestClasses") {
    doLast {
        val testClassesDir = file("${projectDir}/src/test/kotlin")
        val sanityTestClasses = mutableSetOf<String>()
        
        testClassesDir.listFiles()
            ?.filter { it.isFile && (it.name.endsWith("Tests.kt") || it.name.endsWith("Test.kt")) }
            ?.forEach { file ->
                val content = file.readText()
                
                // Extract all class definitions from the file
                val classDefinitions = extractClassDefinitions(content)
                
                // Process each class in the file separately
                classDefinitions.forEach { (className, classContent) ->
                    // Skip this class if it has exclude or unstable tag at the class level
                    if (hasExcludeOrUnstableTag(classContent, atClassLevel = true)) {
                        return@forEach
                    }
                    
                    // Check if the class has at least one @Test method with sanity tag
                    // that doesn't also have unstable or exclude tags
                    val testMethods = classContent.split("@Test")
                        .drop(1) // Drop the first part (before the first @Test)
                    
                    // Check if at least one test method has sanity tag and doesn't have unstable or exclude tags
                    val hasValidSanityTest = testMethods.any { testMethod ->
                        // Check the text between @Test and the next function declaration (fun)
                        val testDeclaration = testMethod.substringBefore("fun")
                        hasSanityTag(testDeclaration) && !hasExcludeOrUnstableTag(testDeclaration)
                    }
                    
                    if (hasValidSanityTest) {
                        // Use the class name if available, otherwise fall back to file name
                        val testClassName = className ?: file.nameWithoutExtension
                        sanityTestClasses.add(testClassName)
                    }
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
