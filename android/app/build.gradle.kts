plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "com.bgnconnect.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.bgnconnect.app"
        minSdk = 29          // Android 10 — the version where background clipboard was locked down
        targetSdk = 35
        versionCode = 2
        versionName = "0.2.0"

        // WebRTC ships native .so per ABI; ship only the ABIs real phones use to
        // keep the APK from ballooning (drops x86/x86_64 emulator builds).
        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a")
        }
    }

    buildFeatures {
        aidl = true          // AGP 8 disables AIDL by default; we need it for Shizuku + hidden stubs
        buildConfig = true
        compose = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
    implementation("androidx.lifecycle:lifecycle-service:2.8.7")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.7")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.security:security-crypto:1.1.0-alpha06")

    // Jetpack Compose (Material 3)
    val composeBom = platform("androidx.compose:compose-bom:2024.12.01")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("com.google.android.material:material:1.12.0") // XML theme parent for the manifest

    // Shizuku — run a UserService as the shell (uid 2000) user for background clipboard.
    implementation("dev.rikka.shizuku:api:13.1.5")
    implementation("dev.rikka.shizuku:provider:13.1.5")

    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    // QR pairing: scan (ScanContract) + display (BarcodeEncoder)
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    // WebRTC (maintained fork of org.webrtc) — direct P2P file transfer DataChannel.
    implementation("io.github.webrtc-sdk:android:125.6422.07")

    testImplementation("junit:junit:4.13.2")
}
