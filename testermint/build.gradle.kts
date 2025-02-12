plugins {
    kotlin("jvm") version "2.0.10"
}

group = "com.productscience"
version = "1.0-SNAPSHOT"

repositories {
    mavenCentral()
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
    testImplementation(kotlin("test"))
    // Add AssertJ for fluent assertions
    testImplementation("org.assertj:assertj-core:3.26.3")
    implementation("org.wiremock:wiremock:3.10.0")
}

tasks.test {
    useJUnitPlatform()
}
kotlin {
    jvmToolchain(19)
}
