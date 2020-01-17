export CC=$HOME/clangwrap
export GO_BUILDER_ENV=host-android-arm64-corellium-android
(
	flock -n 9 || exit 0
	while true; do
		go get golang.org/x/build/cmd/buildlet
		# unset LD_PRELOAD libtermux-exec for 32-bit binaries
		(unset LD_PRELOAD && 
			$HOME/go/bin/buildlet -reverse-type host-android-arm64-corellium-android -coordinator farmer.golang.org)
		sleep 1
		#/system/bin/reboot
	done
) 9>$PREFIX/tmp/builder.lock
