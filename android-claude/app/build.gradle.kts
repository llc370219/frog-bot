plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val frogVersion = providers.gradleProperty("frogVersion").getOrElse("0.1.9")
val versionComponent = "(0|[1-9][0-9]{0,2})"
require(Regex("$versionComponent\\.$versionComponent\\.$versionComponent").matches(frogVersion)) {
    "frogVersion must be a stable MAJOR.MINOR.PATCH version with components from 0 to 999"
}
val (major, minor, patch) = frogVersion.split('.').map(String::toInt)
val frogVersionCode = major * 1_000_000 + minor * 1_000 + patch
require(frogVersionCode > 0) { "frogVersion must be greater than 0.0.0" }

val releaseKeystore = providers.environmentVariable("FROG_ANDROID_KEYSTORE").orNull

android {
    namespace = "io.github.llc370219.frogbot.claude"
    compileSdk = 35

    defaultConfig {
        applicationId = "io.github.llc370219.frogbot.claude"
        minSdk = 26
        targetSdk = 35
        versionCode = frogVersionCode
        versionName = frogVersion
        buildConfigField("boolean", "OFFICIAL_UPDATES", "false")
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storeType = "PKCS12"
                storePassword = providers.environmentVariable("FROG_ANDROID_KEYSTORE_PASSWORD").get()
                keyAlias = providers.environmentVariable("FROG_ANDROID_KEY_ALIAS").get()
                keyPassword = providers.environmentVariable("FROG_ANDROID_KEY_PASSWORD").get()
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName(if (releaseKeystore != null) "release" else "debug")
            buildConfigField("boolean", "OFFICIAL_UPDATES", (releaseKeystore != null).toString())
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures {
        compose = true
        buildConfig = true
    }

    sourceSets["main"].assets.srcDir(layout.buildDirectory.dir("generated/frogAssets"))
    sourceSets["main"].res.srcDir(layout.buildDirectory.dir("generated/frogRes"))
}

// 网页版的 Bot 头像、Agent 图标和青蛙图标直接从 web/public 复制进 App
val copyFrogAssets by tasks.registering(Copy::class) {
    from(rootProject.file("../web/public/assets")) {
        include("bot-avatars/**", "agent-icons/*.png")
    }
    into(layout.buildDirectory.dir("generated/frogAssets"))
}
val copyFrogIcon by tasks.registering(Copy::class) {
    from(rootProject.file("../web/public/frog.png")) { rename { "ic_launcher.png" } }
    into(layout.buildDirectory.dir("generated/frogRes/mipmap-xxxhdpi"))
}
tasks.named("preBuild") { dependsOn(copyFrogAssets, copyFrogIcon) }

dependencies {
    val bom = platform("androidx.compose:compose-bom:2024.12.01")
    implementation(bom)
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
}
