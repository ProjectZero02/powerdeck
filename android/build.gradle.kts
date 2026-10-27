// AGP 9 compiles Kotlin itself ("built-in Kotlin"); putting KGP on the build classpath selects the
// Kotlin version, which must match the Compose compiler plugin's.
buildscript {
    dependencies {
        classpath(libs.kotlin.gradle.plugin)
    }
}

plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.compose) apply false
    alias(libs.plugins.roborazzi) apply false
}
