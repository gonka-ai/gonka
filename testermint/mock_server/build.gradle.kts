plugins {
    kotlin("jvm") version "2.0.10"
    application
    id("io.ktor.plugin") version "2.3.9"
    kotlin("plugin.serialization") version "2.0.10"
}

group = "com.productscience"
version = "1.0-SNAPSHOT"

repositories {
    mavenCentral()
}

dependencies {
    // Ktor server dependencies
    implementation("io.ktor:ktor-server-core:2.3.9")
    implementation("io.ktor:ktor-server-netty:2.3.9")
    implementation("io.ktor:ktor-server-content-negotiation:2.3.9")
    implementation("io.ktor:ktor-serialization-jackson:2.3.9")
    implementation("io.ktor:ktor-serialization-kotlinx-json:2.3.9")
    implementation("io.ktor:ktor-server-call-logging:2.3.9")

    // Ktor client dependencies
    implementation("io.ktor:ktor-client-core:2.3.9")
    implementation("io.ktor:ktor-client-cio:2.3.9")
    implementation("io.ktor:ktor-client-content-negotiation:2.3.9")
    implementation("io.ktor:ktor-client-json:2.3.9")

    // Jackson for JSON processing
    implementation("com.fasterxml.jackson.module:jackson-module-kotlin:2.15.2")

    // JTokkit for tokenization
    implementation("com.knuddels:jtokkit:0.6.1")

    // Logging
    implementation("ch.qos.logback:logback-classic:1.4.14")
    implementation("org.slf4j:slf4j-api:2.0.9")

    // Testing
    testImplementation("io.ktor:ktor-server-test-host:2.3.9")
    testImplementation("org.jetbrains.kotlin:kotlin-test:2.0.10")
    testImplementation("org.assertj:assertj-core:3.26.3")
}

application {
    mainClass.set("com.productscience.mockserver.ApplicationKt")
}

kotlin {
    jvmToolchain(21)
}

tasks.test {
    useJUnitPlatform()
}
