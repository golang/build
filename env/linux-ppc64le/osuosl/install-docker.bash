#!/bin/bash

# Just the docker setup, ending at adding our regular user to the docker group.
# The setup.bash script does everything else.

set -e
sudo apt-get update
sudo apt-get --yes upgrade
sudo apt-get --yes install docker.io
sudo usermod -aG docker $USER
