import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// The client logic: a port of apple/RAKit (and web/src/protocol + web/src/store).
// Plain Kotlin, so its tests run on the JVM without an emulator.
plugins {
    alias(libs.plugins.kotlin.jvm)
    alias(libs.plugins.kotlin.serialization)
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

kotlin {
    compilerOptions { jvmTarget = JvmTarget.JVM_17 }
}

dependencies {
    api(libs.kotlinx.coroutines.core)
    api(libs.kotlinx.serialization.json)
    api(libs.okhttp)

    testImplementation(libs.kotlin.test)
    testImplementation(platform(libs.junit.bom))
    testImplementation(libs.junit.jupiter)
    testRuntimeOnly(libs.junit.platform.launcher)
    testImplementation(libs.kotlinx.coroutines.test)
    testImplementation(libs.okhttp.mockwebserver)
}

tasks.test {
    useJUnitPlatform()
    // The golden fixtures rad's tests write (cd server && go test ./internal/api -update).
    val fixtures = layout.projectDirectory.dir("../../protocol/fixtures")
    inputs.dir(fixtures).withPropertyName("fixtures")
    systemProperty("fixtures", fixtures.asFile.path)
    // The cases every client's shared formatting checks, such as usage_limits.json.
    val vectors = layout.projectDirectory.dir("../../protocol/vectors")
    inputs.dir(vectors).withPropertyName("vectors")
    systemProperty("vectors", vectors.asFile.path)
    // LiveSmokeTest runs only against a scratch rad: RA_PAIR_LINK="$(rad pair --print-url)".
    providers.environmentVariable("RA_PAIR_LINK").orNull?.let { environment("RA_PAIR_LINK", it) }
    providers.environmentVariable("RA_PROJECT").orNull?.let { environment("RA_PROJECT", it) }
}
