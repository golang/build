#!/bin/sh
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

#
# For initial hand testing a newly created/configured test/debug VM. Set the
# environment variables below before running this script:
#
#    ACCOUNT             account to use when ssh'ing to the VM
#    VM_IP_ADDRESS       public IP address of VM
#
# Note that as the script runs it will invoke "ssh", which will require the
# invoker to enter the VM account password several times. TODO: use -M and
# -S ssh flags to avoid reauthentication.
#
# This script also uses "gsutil" to copy things from the go-builder GCS bucket
# as part of the setup.
#
#-----------------------------
#
function checkvarpresent() {
  local TAG="$1"
  local WHICH="$2"
  if [ -z "$WHICH" ]; then
    echo "error: set env var $TAG before running this script"
    exit 1
  fi
}
#
function copy_file_to_vm() {
  local FILE="$1"
  local TGT="$2"
  local SPATH="scp://${ACCOUNT}@${VM_IP_ADDRESS}/${TGT}"
  echo "... executing: scp $FILE $SPATH"
  scp $FILE $SPATH
  if [ $? != 0 ]; then
    echo "** copy failed, aborting"
    exit 1
  fi
}
function run_command_on_vm() {
  local CMD="$*"
  echo "... executing: ssh ${ACCOUNT}@${VM_IP_ADDRESS} $CMD"
  ssh ${ACCOUNT}@${VM_IP_ADDRESS} $CMD
  if [ $? != 0 ]; then
    echo "** command failed, aborting"
    exit 1
  fi
}
function copy_from_go_builder_data() {
  local FILE="$1"
  local TGT="$2"
  echo "... executing: gsutil cp gs://go-builder-data/${FILE} $TGT"
  gsutil cp gs://go-builder-data/${FILE} $TGT
  if [ $? != 0 ]; then
    echo "error: copy from gs://go-builder-data/${FILE} failed, aborting"
    exit 1
  fi
}
#
checkvarpresent ACCOUNT "$ACCOUNT"
checkvarpresent VM_IP_ADDDRESS "$VM_IP_ADDRESS"
#
# Create various directories on the VM.
#
TF=`mktemp /tmp/mkdirsbat.XXXXXXXXXX`
cat >$TF<<EOF
rmdir /s /q C:\Windows\Temp\go
rmdir /s /q C:\Windows\Temp\gobootstrap
mkdir C:\Windows\Temp\go
mkdir C:\Windows\Temp\gobootstrap
EOF
echo "... creating go and gobootstrap directories on VM"
SCRIPT="C:\Windows\Temp\mkdirs.bat"
copy_file_to_vm $TF $SCRIPT
rm -f $TF
run_command_on_vm $SCRIPT
echo "... dir creation on vm complete."
#
# Collect windows bootstrap go to use with all.bat
#
TF2=`mktemp /tmp/bootstrapgo.XXXXXXXXXX`
echo "... copying bootstrap Go from GCS bucket to local path"
copy_from_go_builder_data gobootstrap-windows-arm64-go1.17.13.tar.gz $TF2
#
# Copy the bootstrap Go tar file to the VM.
#
echo "... copying bootstrap Go to VM"
BOOTGOLOC="C:\Windows\Temp\bootgo.tgz"
copy_file_to_vm $TF2 $BOOTGOLOC
rm -f $TF2
echo "... finished copying bootstrap Go to VM"
#
# Unpack the bootstrap Go on the VM
#
echo "... unpacking bootstrap Go on VM"
run_command_on_vm "C:\golang\bootstrap.exe --untar-file=${BOOTGOLOC} --untar-dest-dir=C:\Windows\Temp\gobootstrap"
echo "... finished unpacking bootstrap Go on VM"
#
# Clone Go repo at head, dump a dummy version in it.
#
echo "... starting clone of Go repo"
TF3=`mktemp -d /tmp/gorepo.XXXXXXXXXX`
TF4=`mktemp /tmp/go.XXXXXXXXXX.tgz`
mkdir $TF3/go
git clone --depth=1  https://go.googlesource.com/go $TF3/go
if [ $? != 0 ]; then
  echo "error: git clone failed (git clone --depth=1  https://go.googlesource.com/go $TF3/go)"
  exit 1
fi
echo -n devel gomote.XXXXX > $TF3/go/VERSION
echo -n devel gomote.XXXXX > $TF3/go/VERSION.cache
rm -rf $TF3/go/.git
echo "... finished clone and setup of Go repo"
#
# Tar up the Go repo and copy it to the VM
#
echo "... tar up go repo"
(cd $TF3 ; tar zcf - ./go) > $TF4
echo "... copying go repo tar file to VM"
GOTIPLOC="C:\Windows\Temp\gotip.tgz"
copy_file_to_vm $TF4 $GOTIPLOC
rm -f $TF4
rm -rf $TF3
#
# Unpack on the VM
#
echo "... unpacking Go repo tar file on vm"
run_command_on_vm "C:\golang\bootstrap.exe --untar-file=${GOTIPLOC} --untar-dest-dir=C:\Windows\Temp\go"
#
# Create command to run all.bat
#
TF5=`mktemp /tmp/runallbat.XXXXXXXXXX`
echo "... creating bat script to run all.bat"
cat >$TF5<<EOF
cd C:\Windows\Temp\go\go\src
set PATH=%PATH%;C:\godep\llvm-aarch64\bin
set GOROOT_BOOTSTRAP=C:\Windows\Temp\gobootstrap
all.bat
EOF
#
# Copy script to VM
#
echo "... copying all.bat script to VM"
ALLBATSCRIPT="C:\Windows\Temp\runall.bat"
copy_file_to_vm $TF5 $ALLBATSCRIPT
rm -f $TF5
#
# Execute
#
echo "... running all.bat invocation script"
run_command_on_vm $ALLBATSCRIPT
echo "done."
exit 0
