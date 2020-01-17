#!/bin/bash

# install.sh sets up newly installed Android Corellium device.
# Connect to the device with "adb connect <ip>:5001" where
# <ip> is the device adb address.
#
# place a valid builder key in the `buildkey` file.

curl -o com.termux.apk "https://f-droid.org/repo/com.termux_77.apk"
curl -o com.termux.boot.apk "https://f-droid.org/repo/com.termux.boot_7.apk"

adb install com.termux.apk

# Run Termux to set up filesystem.
adb shell monkey -p com.termux -c android.intent.category.LAUNCHER 1

adb install com.termux.boot.apk

# Run boot app once to enable run-on-boot.
adb shell monkey -p com.termux.boot -c android.intent.category.LAUNCHER 1

adb root

# Wait for the Termux filesystem.
while adb shell ls /data/data/com.termux/files/home 2> /dev/null ; [ $? -ne 0 ]; do
	sleep 1
done

adb push buildkey /data/data/com.termux/files/home/.gobuildkey-host-android-arm64-corellium-android
adb push files/exec.sh /data/data/com.termux
adb push files/clangwrap.go /data/data/com.termux/files/home
adb push files/builder.sh /data/data/com.termux/files/home
adb push files/profile /data/data/com.termux/files/home/.profile
adb shell chmod +x /data/data/com.termux/exec.sh
adb shell chmod +x /data/data/com.termux/files/home/builder.sh
# Get Termux username.
USER=$(adb shell stat -c "%U" /data/data/com.termux)

termux() {
	adb shell su "$USER" /data/data/com.termux/exec.sh "$@"
}

termux mkdir -p /data/data/com.termux/files/home/tmpdir
# Run builder at boot.
termux mkdir -p /data/data/com.termux/files/home/.termux/boot
adb push files/run-builder-at-boot /data/data/com.termux/files/home/.termux/boot
termux pkg install -y openssh tmux ndk-multilib clang git golang lld
termux go build clangwrap.go

# Move the arm 32-bit sysroot so 32-bit arm binaries use the Android
# system libraries at runtime, not those in the build sysroot. If
# we don't, runtime linking fails.
termux mv /data/data/com.termux/files/usr/arm-linux-androideabi /data/data/com.termux/files/home/arm-linux-androideabi

termux /system/bin/reboot

