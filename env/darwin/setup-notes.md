# For all machine types

- Turn on the computer.
- Click through setup, connect to wifi, etc.
- Full name: Gopher Gopherson
- Account name: gopher
- Password: with an exclamation mark
- Decline as much as possible.
- Set time zone to NY.
- Open a terminal.
- `sudo visudo`

  Change  `%admin ALL=(ALL) ALL` to `%admin ALL=(ALL) NOPASSWD: ALL`.

- `sudo nvram boot-args="-v"`

- Install Go: download the latest tarball from go.dev/dl.

  `tar -xf Downloads/go*.darwin-*.tar`

  `mv go $HOME/goboot`

Create `$HOME/stage0.sh`.

**For physical machines**
```
#!/bin/bash

set -x

mkdir -p ~/go/bin;
while true; do
  rm -f ~/go/bin/buildlet
  url="https://storage.googleapis.com/go-builder-data/buildlet.darwin-arm64"
  while ! curl -f -o ~/go/bin/buildlet "$url"; do
      echo
      echo "curl failed to fetch $url"
      echo "Sleeping before retrying..."
      sleep 5
  done
  chmod +x ~/go/bin/buildlet

  mkdir -p /tmp/buildlet
  ~/go/bin/buildlet --coordinator=farmer.golang.org --reverse-type host-darwin-arm64-XX_0 --halt=false --workdir=/tmp/buildlet;
   sleep 2;
done
```

**For QEMU VMs**
```
#!/bin/bash

set -x

export GO_BUILDER_ENV=qemu_vm

mkdir -p ~/go/bin;
while true; do
  rm -f ~/go/bin/buildlet
  url="https://storage.googleapis.com/go-builder-data/buildlet.darwin-arm64"
  while ! curl -f -o ~/go/bin/buildlet "$url"; do
      echo
      echo "curl failed to fetch $url"
      echo "Sleeping before retrying..."
      sleep 5
  done
  chmod +x ~/go/bin/buildlet

  mkdir -p /tmp/buildlet
  ~/go/bin/buildlet --coordinator=farmer.golang.org --reverse-type host-darwin-arm64-XX-aws --halt=true --workdir=/tmp/buildlet;
   sleep 2;
done
```

`chmod +x $HOME/stage0.sh`

- Run Automator.
- Create a new Application.
- Add a "run shell script" item with the command:
  `open -a Terminal.app $HOME/stage0.sh`
- Save it to the desktop as "run-builder".

In System Preferences:
- Software Update > Advanced > disable checking for updates
- Desktop & Screensaver > uncheck show screensaver
- Energy Saver > never turn off display, don't automatically sleep, start up after power failure
- Sharing > enable ssh (leave the default administrators setting)
- Users & Groups > Gopher Gopherson > Login Items > add run-builder
- General (before 13: Users & Groups) > Login Options > auto-login Gopher Gopherson
- Network -> Ethernet -> Advanced -> DNS -> Add DNS server -> 8.8.8.8
  - Only necessary on AWS guests, and until https://go.dev/issue/36718 is
    resolved on all tested releases.

Install XCode:
- Download Xcode from the Apple Developer site:
https://stackoverflow.com/questions/10335747/how-to-download-xcode-dmg-or-xip-file.
https://developer.apple.com/support/xcode/ is a more authoritative list of versions.
(You don't want to log in to your account on the machine, so don't use the App Store.)
- Extract it (`xip -x file.xip`) and move the resulting Xcode folder to Applications
- run xcode-select: `sudo xcode-select --switch /Applications/Xcode.app`
- run `xcodebuild -version` and wait for Xcode to be verified, which will take a long time.
- accept the license: `sudo xcodebuild -license accept`
- install the command line tools: `sudo xcode-select --install`
- run xcode-select: `sudo xcode-select --switch /Library/Developer/CommandLineTools`



Install crypto/x509 test root:
```
curl -f -o /tmp/test_root.pem "https://storage.googleapis.com/go-builder-data/platform_root_cert.pem"
security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain /tmp/test_root.pem
```

Put a builder key in the usual spot.
