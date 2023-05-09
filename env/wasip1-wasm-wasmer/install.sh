#!/bin/sh

# This install script is intended to download and install the latest available
# release of Wasmer.
# It attempts to identify the current platform and an error will be thrown if
# the platform is not supported.
#
# Environment variables:
# - WASMER_DIR (optional): defaults to $HOME/.wasmer
#
# You can install using this script:
# $ curl https://raw.githubusercontent.com/wasmerio/wasmer-install/master/install.sh | sh

# Installer script inspired by:
#  1) https://raw.githubusercontent.com/golang/dep/master/install.sh
#  2) https://sh.rustup.rs
#  3) https://yarnpkg.com/install.sh
#  4) https://raw.githubusercontent.com/brainsik/virtualenv-burrito/master/virtualenv-burrito.sh

reset="\033[0m"
red="\033[31m"
green="\033[32m"
yellow="\033[33m"
white="\033[37m"
bold="\e[1m"
dim="\e[2m"

RELEASES_URL="https://github.com/wasmerio/wasmer/releases"
WAPM_RELEASES_URL="https://github.com/wasmerio/wapm-cli/releases"

WASMER_VERBOSE="verbose"
if [ -z "$WASMER_INSTALL_LOG" ]; then
  WASMER_INSTALL_LOG="$WASMER_VERBOSE"
fi

wasmer_download_json() {
  url="$2"

  # echo "Fetching $url.."
  if test -x "$(command -v curl)"; then
    response=$(curl -s -L -w 'HTTPSTATUS:%{http_code}' -H 'Accept: application/json' "$url")
    body=$(echo "$response" | sed -e 's/HTTPSTATUS\:.*//g')
    code=$(echo "$response" | tr -d '\n' | sed -e 's/.*HTTPSTATUS://')
  elif test -x "$(command -v wget)"; then
    temp=$(mktemp)
    body=$(wget -q --header='Accept: application/json' -O - --server-response "$url" 2>"$temp")
    code=$(awk '/^  HTTP/{print $2}' <"$temp" | tail -1)
    rm "$temp"
  else
    wasmer_error "Neither curl nor wget was available to perform http requests"
    return 1
  fi
  if [ "$code" != 200 ]; then
    wasmer_error "File download failed with code $code"
    return 1
  fi

  eval "$1='$body'"
  return 0
}

wasmer_download_file() {
  url="$1"
  destination="$2"

  # echo "Fetching $url.."
  if test -x "$(command -v curl)"; then
    if [ "$WASMER_INSTALL_LOG" = "$WASMER_VERBOSE" ]; then
      code=$(curl --progress-bar -w '%{http_code}' -L "$url" -o "$destination")
      printf "\033[K\n\033[1A"
    else
      code=$(curl -s -w '%{http_code}' -L "$url" -o "$destination")
    fi
  elif test -x "$(command -v wget)"; then
    if [ "$WASMER_INSTALL_LOG" = "$WASMER_VERBOSE" ]; then
      code=$(wget --show-progress --progress=bar:force:noscroll -q -O "$destination" --server-response "$url" 2>&1 | awk '/^  HTTP/{print $2}' | tail -1)
      printf "\033[K\n\033[1A"
    else
      code=$(wget --quiet -O "$destination" --server-response "$url" 2>&1 | awk '/^  HTTP/{print $2}' | tail -1)
    fi
  else
    wasmer_error "Neither curl nor wget was available to perform http requests."
    return 1
  fi

  if [ "$code" = 404 ]; then
    wasmer_error "Your platform is not yet supported ($OS-$ARCH).$reset\nPlease open an issue on the project if you would like to use wasmer in your project: https://github.com/wasmerio/wasmer"
    return 1
  elif [ "$code" != 200 ]; then
    wasmer_error "File download failed with code $code"
    return 1
  fi
  return 0
}

wasmer_detect_profile() {
  if [ -n "${PROFILE}" ] && [ -f "${PROFILE}" ]; then
    echo "${PROFILE}"
    return
  fi

  local DETECTED_PROFILE
  DETECTED_PROFILE=''
  local SHELLTYPE
  SHELLTYPE="$(basename "/$SHELL")"

  if [ "$SHELLTYPE" = "bash" ]; then
    if [ -f "$HOME/.bashrc" ]; then
      DETECTED_PROFILE="$HOME/.bashrc"
    elif [ -f "$HOME/.bash_profile" ]; then
      DETECTED_PROFILE="$HOME/.bash_profile"
    fi
  elif [ "$SHELLTYPE" = "zsh" ]; then
    DETECTED_PROFILE="$HOME/.zshrc"
  elif [ "$SHELLTYPE" = "fish" ]; then
    DETECTED_PROFILE="$HOME/.config/fish/config.fish"
  fi

  if [ -z "$DETECTED_PROFILE" ]; then
    if [ -f "$HOME/.profile" ]; then
      DETECTED_PROFILE="$HOME/.profile"
    elif [ -f "$HOME/.bashrc" ]; then
      DETECTED_PROFILE="$HOME/.bashrc"
    elif [ -f "$HOME/.bash_profile" ]; then
      DETECTED_PROFILE="$HOME/.bash_profile"
    elif [ -f "$HOME/.zshrc" ]; then
      DETECTED_PROFILE="$HOME/.zshrc"
    elif [ -f "$HOME/.config/fish/config.fish" ]; then
      DETECTED_PROFILE="$HOME/.config/fish/config.fish"
    fi
  fi

  if [ ! -z "$DETECTED_PROFILE" ]; then
    echo "$DETECTED_PROFILE"
  fi
}

wasmer_link() {

  WASMER_PROFILE="$(wasmer_detect_profile)"

  LOAD_STR="\n# Wasmer\nexport WASMER_DIR=\"$INSTALL_DIRECTORY\"\n[ -s \"\$WASMER_DIR/wasmer.sh\" ] && source \"\$WASMER_DIR/wasmer.sh\"\n"
  SOURCE_STR="# Wasmer config\nexport WASMER_DIR=\"$INSTALL_DIRECTORY\"\nexport WASMER_CACHE_DIR=\"\$WASMER_DIR/cache\"\nexport PATH=\"\$WASMER_DIR/bin:\$PATH:\$WASMER_DIR/globals/wapm_packages/.bin\"\n"

  # We create the wasmer.sh file
  printf "$SOURCE_STR" >"$INSTALL_DIRECTORY/wasmer.sh"

  if [ -z "${WASMER_PROFILE-}" ]; then
    wasmer_error "Profile not found. Tried:\n* ${WASMER_PROFILE} (as defined in \$PROFILE)\n* ~/.bashrc\n* ~/.bash_profile\n* ~/.zshrc\n* ~/.profile.\n${reset}Append the following lines to the correct file yourself:\n${SOURCE_STR}"
    return 1
  else
    printf "Updating bash profile $WASMER_PROFILE\n"
    if ! grep -q 'wasmer.sh' "$WASMER_PROFILE"; then
      # if [[ $WASMER_PROFILE = *"fish"* ]]; then
      #   command fish -c 'set -U fish_user_paths $fish_user_paths ~/.wasmer/bin'
      # else
      command printf "$LOAD_STR" >>"$WASMER_PROFILE"
      # fi
      if [ "$WASMER_INSTALL_LOG" = "$WASMER_VERBOSE" ]; then
        printf "we've added the following to your $WASMER_PROFILE\n"
        echo "If you have a different profile please add the following:"
        printf "$dim$LOAD_STR$reset"
      fi
      wasmer_fresh_install=true
    else
      wasmer_warning "the profile already has Wasmer and has not been changed"
    fi

    version=$($INSTALL_DIRECTORY/bin/wasmer --version) || (
      wasmer_error "wasmer was installed, but doesn't seem to be working :("
      return 1
    )

    wasmer_install_status "check" "$version installed successfully ✓"

    if [ "$WASMER_INSTALL_LOG" = "$WASMER_VERBOSE" ]; then
      if [ "$wasmer_fresh_install" = true ]; then
        printf "wasmer & wapm will be available the next time you open the terminal.\n"
        printf "If you want to have the commands available now please execute:\n\nsource $INSTALL_DIRECTORY/wasmer.sh$reset\n"
      fi
    fi
  fi
  return 0
}

initArch() {
  ARCH=$(uname -m)
  case $ARCH in
  amd64) ARCH="amd64" ;;
  x86_64) ARCH="amd64" ;;
  aarch64) ARCH="aarch64" ;;
  riscv64) ARCH="riscv64" ;;
  arm64) ARCH="arm64" ;; # This is for the macOS M1 ARM chips
  *)
    wasmer_error "The system architecture (${ARCH}) is not yet supported by this installation script."
    exit 1
    ;;
  esac
  # echo "ARCH = $ARCH"
}

initOS() {
  OS=$(uname | tr '[:upper:]' '[:lower:]')
  case "$OS" in
  darwin) OS='darwin' ;;
  linux) OS='linux' ;;
  freebsd) OS='freebsd' ;;
  # mingw*) OS='windows';;
  # msys*) OS='windows';;
  *)
    printf "$red> The OS (${OS}) is not supported by this installation script.$reset\n"
    exit 1
    ;;
  esac
}

wasmer_install() {
  magenta1="${reset}\033[34;1m"
  magenta2=""
  magenta3=""

  if which wasmer >/dev/null; then
    printf "${reset}Welcome to the Wasmer bash installer!$reset\n"
  else
    printf "${reset}Welcome to the Wasmer bash installer!$reset\n"
    if [ "$WASMER_INSTALL_LOG" = "$WASMER_VERBOSE" ]; then
      printf "
${magenta1}               ww
${magenta1}               wwwww
${magenta1}        ww     wwwwww  w
${magenta1}        wwwww      wwwwwwwww
${magenta1}ww      wwwwww  w     wwwwwww
${magenta1}wwwww      wwwwwwwwww   wwwww
${magenta1}wwwwww  w      wwwwwww  wwwww
${magenta1}wwwwwwwwwwwwww   wwwww  wwwww
${magenta1}wwwwwwwwwwwwwww  wwwww  wwwww
${magenta1}wwwwwwwwwwwwwww  wwwww  wwwww
${magenta1}wwwwwwwwwwwwwww  wwwww  wwwww
${magenta1}wwwwwwwwwwwwwww  wwwww   wwww
${magenta1}wwwwwwwwwwwwwww  wwwww
${magenta1}   wwwwwwwwwwww   wwww
${magenta1}       wwwwwwww
${magenta1}           wwww
${reset}
"
    fi
  fi

  wasmer_download $1 && wasmer_link
  wapm_download
  wasmer_reset
}

wasmer_reset() {
  unset -f wasmer_install semver_compare wasmer_reset wasmer_download_json wasmer_link wasmer_detect_profile wasmer_download_file wasmer_download wasmer_verify_or_quit
}

version() {
  echo "$@" | awk -F. '{ printf("%d%03d%03d%03d\n", $1,$2,$3,$4); }'
}

semverParseInto() {
  local RE='v?([0-9]+)[.]([0-9]+)[.]([0-9]+)([.0-9A-Za-z-]*)'

  # # strip word "v" if exists
  # version=$(echo "${1//v/}")

  #MAJOR
  eval $2=$(echo $1 | sed -E "s#$RE#\1#")
  #MINOR
  eval $3=$(echo $1 | sed -E "s#$RE#\2#")
  #MINOR
  eval $4=$(echo $1 | sed -E "s#$RE#\3#")
  #SPECIAL
  eval $5=$(echo $1 | sed -E "s#$RE#\4#")
}

###
# Code inspired (copied partially and improved) with attributions from:
# https://github.com/cloudflare/semver_bash/blob/master/semver.sh
# https://gist.github.com/Ariel-Rodriguez/9e3c2163f4644d7a389759b224bfe7f3
###
semver_compare() {
  local version_a version_b

  local MAJOR_A=0
  local MINOR_A=0
  local PATCH_A=0
  local SPECIAL_A=0

  local MAJOR_B=0
  local MINOR_B=0
  local PATCH_B=0
  local SPECIAL_B=0

  semverParseInto $1 MAJOR_A MINOR_A PATCH_A SPECIAL_A
  semverParseInto $2 MAJOR_B MINOR_B PATCH_B SPECIAL_B

  # Check if our version is higher
  if [ $MAJOR_A -gt $MAJOR_B ]; then
    echo 1 && return 0
  fi
  if [ $MAJOR_A -eq $MAJOR_B ]; then
    if [ $MINOR_A -gt $MINOR_B ]; then
      echo 1 && return 0
    elif [ $MINOR_A -eq $MINOR_B ]; then
      if [ $PATCH_A -gt $PATCH_B ]; then
        echo 1 && return 0
      elif [ $PATCH_A -eq $PATCH_B ]; then
        if [ -n "$SPECIAL_A" ] && [ -z "$SPECIAL_B" ]; then
          # if the version we're targeting does not have a tag and our current
          # version does, we should upgrade because no tag > tag
          echo -1 && return 0
        elif [ "$SPECIAL_A" \> "$SPECIAL_B" ]; then
          echo 1 && return 0
        elif [ "$SPECIAL_A" = "$SPECIAL_B" ]; then
          # complete match
          echo 0 && return 0
        fi
      fi
    fi
  fi

  # if we're here we know that the target verison cannot be less than or equal to
  # our current version, therefore we upgrade

  echo -1 && return 0
}

wasmer_download() {
  # identify platform based on uname output
  initArch || return 1
  initOS || return 1

  # assemble expected release artifact name
  BINARY="wasmer-${OS}-${ARCH}.tar.gz"

  # add .exe if on windows
  # if [ "$OS" = "windows" ]; then
  #     BINARY="$BINARY.exe"
  # fi

  wasmer_install_status "downloading" "wasmer-$OS-$ARCH"
  if [ $# -eq 0 ]; then
    # The version was not provided, assume latest
    wasmer_download_json LATEST_RELEASE "$RELEASES_URL/latest" || return 1
    WASMER_RELEASE_TAG=$(echo "${LATEST_RELEASE}" | tr -s '\n' ' ' | sed 's/.*"tag_name":"//' | sed 's/".*//')
    printf "Latest release: ${WASMER_RELEASE_TAG}\n"
  else
    WASMER_RELEASE_TAG="${1}"
    printf "Installing provided version: ${WASMER_RELEASE_TAG}\n"
  fi

  if which $INSTALL_DIRECTORY/bin/wasmer >/dev/null; then
    WASMER_VERSION=$($INSTALL_DIRECTORY/bin/wasmer --version | sed 's/wasmer //g')
    printf "Wasmer already installed in ${INSTALL_DIRECTORY} with version: ${WASMER_VERSION}\n"

    WASMER_COMPARE=$(semver_compare $WASMER_VERSION $WASMER_RELEASE_TAG)
    case $WASMER_COMPARE in
    # WASMER_VERSION = WASMER_RELEASE_TAG
    0)
      if [ $# -eq 0 ]; then
        wasmer_warning "wasmer is already installed in the latest version: ${WASMER_RELEASE_TAG}"
      else
        wasmer_warning "wasmer is already installed with the same version: ${WASMER_RELEASE_TAG}"
      fi
      printf "Do you want to force the installation?"
      wasmer_verify_or_quit || return 1
      ;;
      # WASMER_VERSION > WASMER_RELEASE_TAG
    1)
      wasmer_warning "the selected version (${WASMER_RELEASE_TAG}) is lower than current installed version ($WASMER_VERSION)"
      printf "Do you want to continue installing Wasmer $WASMER_RELEASE_TAG?"
      wasmer_verify_or_quit || return 1
      ;;
      # WASMER_VERSION < WASMER_RELEASE_TAG (we continue)
    -1) ;;
    esac
  fi

  # fetch the real release data to make sure it exists before we attempt a download
  wasmer_download_json RELEASE_DATA "$RELEASES_URL/tag/$WASMER_RELEASE_TAG" || return 1

  BINARY_URL="$RELEASES_URL/download/$WASMER_RELEASE_TAG/$BINARY"
  DOWNLOAD_FILE=$(mktemp -t wasmer.XXXXXXXXXX)

  printf "Downloading archive from ${BINARY_URL}\n"

  wasmer_download_file "$BINARY_URL" "$DOWNLOAD_FILE" || return 1
  # echo -en "\b\b"
  printf "\033[K\n\033[1A"

  # windows not supported yet
  # if [ "$OS" = "windows" ]; then
  #     INSTALL_NAME="$INSTALL_NAME.exe"
  # fi

  # echo "Moving executable to $INSTALL_DIRECTORY/$INSTALL_NAME"

  wasmer_install_status "installing" "${INSTALL_DIRECTORY}"

  mkdir -p $INSTALL_DIRECTORY

  # Untar the wasmer contents in the install directory
  tar -C $INSTALL_DIRECTORY -zxf $DOWNLOAD_FILE
  return 0
}

wapm_download() {
  # identify platform based on uname output
  initArch || return 1
  initOS || return 1

  if [ "$ARCH" = "arm64" ]; then
    ARCH="aarch64"
  fi

  # assemble expected release artifact name
  BINARY="wapm-cli-${OS}-${ARCH}.tar.gz"

  wasmer_install_status "downloading" "wapm-cli-$OS-$ARCH"
  # Download latest wapm version
  wasmer_download_json LATEST_RELEASE "$WAPM_RELEASES_URL/latest" || return 1
  WAPM_RELEASE_TAG=$(echo "${LATEST_RELEASE}" | tr -s '\n' ' ' | sed 's/.*"tag_name":"//' | sed 's/".*//' | sed 's/v//g')
  printf "Latest release: ${WAPM_RELEASE_TAG}\n"

  if which $INSTALL_DIRECTORY/bin/wapm >/dev/null; then
    WAPM_VERSION=$($INSTALL_DIRECTORY/bin/wapm --version | sed 's/wapm-cli //g')
    printf "WAPM already installed in ${INSTALL_DIRECTORY} with version: ${WAPM_VERSION}\n"

    WAPM_COMPARE=$(semver_compare $WAPM_VERSION $WAPM_RELEASE_TAG)
    case $WAPM_COMPARE in
    # WAPM_VERSION = WAPM_RELEASE_TAG
    0)
      if [ $# -eq 0 ]; then
        wasmer_warning "WAPM is already installed in the latest version: ${WAPM_RELEASE_TAG}"
      else
        wasmer_warning "WAPM is already installed with the same version: ${WAPM_RELEASE_TAG}"
      fi
      printf "Do you want to force the installation?"
      wasmer_verify_or_quit || return 1
      ;;
      # WAPM_VERSION > WAPM_RELEASE_TAG
    1)
      wasmer_warning "the selected version (${WAPM_RELEASE_TAG}) is lower than current installed version ($WAPM_VERSION)"
      printf "Do you want to continue installing WAPM $WAPM_RELEASE_TAG?"
      wasmer_verify_or_quit || return 1
      ;;
      # WAPM_VERSION < WAPM_RELEASE_TAG (we continue)
    -1) ;;
    esac
  fi

  # fetch the real release data to make sure it exists before we attempt a download
  wasmer_download_json RELEASE_DATA "$WAPM_RELEASES_URL/tag/v$WAPM_RELEASE_TAG" || return 1

  BINARY_URL="$WAPM_RELEASES_URL/download/v$WAPM_RELEASE_TAG/$BINARY"
  DOWNLOAD_FILE=$(mktemp -t wapm.XXXXXXXXXX)

  printf "Downloading archive from ${BINARY_URL}\n"

  wasmer_download_file "$BINARY_URL" "$DOWNLOAD_FILE" || return 1

  printf "\033[K\n\033[1A"

  wasmer_install_status "installing" "${INSTALL_DIRECTORY}"

  mkdir -p $INSTALL_DIRECTORY

  # Untar the WAPM contents in the install directory
  tar -C $INSTALL_DIRECTORY -zxf $DOWNLOAD_FILE
}

wasmer_error() {
  printf "$bold${red}error${white}: $1${reset}\n"
}

wasmer_install_status() {
  printf "$bold${green}${1}${white}: $2${reset}\n"
}

wasmer_warning() {
  printf "$bold${yellow}warning${white}: $1${reset}\n"
}

wasmer_verify_or_quit() {
  if [ -n "$BASH_VERSION" ]; then
    # If we are in bash, we can use read -n
    read -p "$1 [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
      wasmer_error "installation aborted"
      return 1
    fi
    return 0
  fi

  read -p "$1 [y/N]" yn
  case $yn in
  [Yy]*) break ;;
  [Nn]*)
    wasmer_error "installation aborted"
    return 1
    ;;
  *) echo "Please answer yes or no." ;;
  esac

  return 0
}

# determine install directory if required
if [ -z "$WASMER_DIR" ]; then
  # If WASMER_DIR is not present
  INSTALL_DIRECTORY="$HOME/.wasmer"
else
  # If WASMER_DIR is present
  INSTALL_DIRECTORY="${WASMER_DIR}"
fi

wasmer_install $1 # $2
