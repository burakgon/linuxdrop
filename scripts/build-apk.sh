#!/usr/bin/env bash
# Hermetic debug-APK build via Docker — no host JDK/Gradle/SDK changes.
# Uses gradle:8.11.1-jdk21 (JDK 21 + Gradle 8.11.1) and installs Android SDK 35
# into a persistent docker volume (fast re-runs).
#
# Output: android/app/build/outputs/apk/debug/app-debug.apk
set -euo pipefail
cd "$(dirname "$0")/.."

docker run --rm \
  -e ANDROID_HOME=/sdk -e ANDROID_SDK_ROOT=/sdk \
  -e ANDROID_USER_HOME=/sdk/.android \
  -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
  -v "$PWD/android":/work \
  -v bgn-android-sdk:/sdk \
  -w /work \
  gradle:8.11.1-jdk21 \
  bash -c '
    set -e
    if [ ! -d /sdk/platforms/android-35 ]; then
      echo "== Android SDK (35) kuruluyor =="
      apt-get update -qq && apt-get install -y -qq curl unzip >/dev/null
      mkdir -p /sdk/cmdline-tools && cd /tmp
      curl -fsSL -o clt.zip https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip
      unzip -q clt.zip -d /sdk/cmdline-tools
      mv /sdk/cmdline-tools/cmdline-tools /sdk/cmdline-tools/latest
      yes | /sdk/cmdline-tools/latest/bin/sdkmanager --licenses >/dev/null
      /sdk/cmdline-tools/latest/bin/sdkmanager "platform-tools" "platforms;android-35" "build-tools;35.0.0" >/dev/null
    fi
    cd /work
    export GRADLE_USER_HOME=/sdk/.gradle
    echo "== gradle :app:assembleDebug =="
    gradle :app:assembleDebug --no-daemon --stacktrace
    chown -R "$HOST_UID:$HOST_GID" /work
    echo "== APK =="
    ls -la /work/app/build/outputs/apk/debug/
  '
