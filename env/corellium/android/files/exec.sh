#!/system/bin/sh

# Executes command with the Termux environment.

export PREFIX='/data/data/com.termux/files/usr'
export HOME='/data/data/com.termux/files/home'
export LD_LIBRARY_PATH='/data/data/com.termux/files/usr/lib'
export PATH="/data/data/com.termux/files/usr/bin:/data/data/com.termux/files/usr/bin/applets:$PATH"
export TMPDIR=$HOME/tmpdir

cd $HOME

exec "$@"
