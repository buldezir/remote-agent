// AGP 9 compiles Kotlin itself, so there's no org.jetbrains.kotlin.android plugin here.
plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

// Release tags set the version (-PversionName=0.3.0); other builds are 0.1, a development
// build, like the Apple apps built from Xcode. versionCode only ever grows:
// 0.3.0-beta.2 → 30002, 0.3.0 → 30099.
val appVersion = providers.gradleProperty("versionName").getOrElse("0.1")
val appVersionCode = Regex("""^(\d+)\.(\d+)(?:\.(\d+))?(?:-[A-Za-z]*\.?(\d+))?""").find(appVersion)
    ?.destructured?.let { (major, minor, patch, pre) ->
        major.toInt() * 1_000_000 + minor.toInt() * 10_000 + (patch.toIntOrNull() ?: 0) * 100 +
            (pre.toIntOrNull() ?: 99)
    } ?: 1

// The release key lives outside the repo: in CI, from repository secrets; locally, only if
// these are set. Without them assembleRelease makes an unsigned APK.
val keystoreFile = providers.environmentVariable("RA_KEYSTORE_FILE")

android {
    namespace = "dev.remoteagent.app"
    compileSdk = 37

    defaultConfig {
        applicationId = "dev.remoteagent.app"
        minSdk = 36
        targetSdk = 37
        versionCode = appVersionCode
        versionName = appVersion
    }

    signingConfigs {
        if (keystoreFile.isPresent) {
            create("release") {
                storeFile = file(keystoreFile.get())
                storePassword = providers.environmentVariable("RA_KEYSTORE_PASSWORD").get()
                keyAlias = providers.environmentVariable("RA_KEY_ALIAS").get()
                keyPassword = providers.environmentVariable("RA_KEY_PASSWORD").get()
            }
        }
    }

    buildTypes {
        debug {
            // Installs next to a release build.
            applicationIdSuffix = ".debug"
        }
        create("beta") {
            initWith(getByName("debug"))
            applicationIdSuffix = ".beta"
            versionNameSuffix = "-beta"
            matchingFallbacks += "debug"
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
    }
}

dependencies {
    implementation(project(":core"))
    implementation(libs.kotlinx.coroutines.android)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.foundation)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.adaptive)
    implementation(libs.androidx.compose.adaptive.layout)
    implementation(libs.androidx.compose.adaptive.navigation3)
    implementation(libs.androidx.compose.ui.tooling.preview)
    debugImplementation(libs.androidx.compose.ui.tooling)
    implementation(libs.androidx.navigation3.runtime)
    implementation(libs.androidx.navigation3.ui)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.process)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.datastore.preferences)
    implementation(libs.coil.compose)
    implementation(libs.coil.network.okhttp)
    implementation(libs.telephoto.zoomable)
    implementation(libs.markdown.m3)
    implementation(libs.termlib)
    implementation(libs.play.services.code.scanner)

    testImplementation(libs.kotlin.test.junit)
}
