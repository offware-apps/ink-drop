#!/usr/bin/env bash
# Builds and signs android/build/ink-drop.apk from the stages in cmd/ and the
# shell in android/src, with the Android SDK's own tools and no Gradle.
# The signing key lives in ~/.config/ink-drop/, outside every repository.
set -euo pipefail

cd "$(dirname "$0")"
SDK=${ANDROID_HOME:-$HOME/Android/Sdk}
TOOLS=$SDK/build-tools/36.0.0
JAR=$SDK/platforms/android-36/android.jar
KEYS=$HOME/.config/ink-drop
OUT=build

rm -rf "$OUT"
mkdir -p "$OUT/apk/lib/arm64-v8a" "$OUT/classes"

# Each stage ships as lib<name>.so, the one place Android lets an app run a file.
for stage in ink-fetch ink-readable ink-epub ink-feed; do
  CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags='-s -w' \
    -o "$OUT/apk/lib/arm64-v8a/lib${stage//-/}.so" "../cmd/$stage"
done

javac --release 17 -classpath "$JAR" -d "$OUT/classes" $(find src -name '*.java')
# The app's own rules run here, on the laptop, before anything is packed.
javac --release 17 -classpath "$OUT/classes:$JAR" -d "$OUT/test" $(find test -name '*.java')
java -cp "$OUT/test:$OUT/classes:$JAR" io.github.offwareapps.inkdrop.RulesTest
"$TOOLS/d8" --min-api 33 --lib "$JAR" --output "$OUT/apk" $(find "$OUT/classes" -name '*.class')
"$TOOLS/aapt2" compile --dir res -o "$OUT/res.zip"
"$TOOLS/aapt2" link -o "$OUT/unsigned.apk" --manifest AndroidManifest.xml -I "$JAR" -A assets "$OUT/res.zip" \
  --min-sdk-version 33 --target-sdk-version 36
jar --update --no-compress --file "$OUT/unsigned.apk" -C "$OUT/apk" .
"$TOOLS/zipalign" -f -p 4 "$OUT/unsigned.apk" "$OUT/aligned.apk"

if [ ! -f "$KEYS/android.keystore" ]; then
  mkdir -p "$KEYS" && chmod 700 "$KEYS"
  (umask 077 && head -c 24 /dev/urandom | base64 > "$KEYS/android.keystore.pass")
  keytool -genkeypair -keystore "$KEYS/android.keystore" -storepass:file "$KEYS/android.keystore.pass" \
    -alias ink-drop -keyalg EC -groupname secp256r1 -validity 10000 -dname "CN=ink-drop"
fi
"$TOOLS/apksigner" sign --ks "$KEYS/android.keystore" --ks-pass "file:$KEYS/android.keystore.pass" \
  --out "$OUT/ink-drop.apk" "$OUT/aligned.apk"
echo "$OUT/ink-drop.apk"
