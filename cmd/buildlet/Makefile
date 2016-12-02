usage: FORCE
	# See targets in Makefile (e.g. "buildlet.darwin-amd64")
	exit 1

FORCE:

buildlet.darwin-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.darwin-amd64.gz: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --gzip --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.freebsd-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-amd64-static: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false --static go-builder-data/$@

buildlet.netbsd-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.openbsd-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.openbsd-386: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.plan9-386: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.windows-amd64: FORCE buildlet_windows.go
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-arm: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-arm-arm5: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=linux-arm --installsuffix=arm5 --extraenv=GOARM=5 --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-arm64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-mips: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-mipsle: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-mips64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-mips64le: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-ppc64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-ppc64le: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.linux-s390x: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.solaris-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false go-builder-data/$@

buildlet.all: FORCE buildlet.darwin-amd64 buildlet.darwin-amd64.gz buildlet.freebsd-amd64 buildlet.linux-amd64 buildlet.netbsd-amd64 buildlet.openbsd-amd64 buildlet.openbsd-386 buildlet.plan9-386 buildlet.windows-amd64 buildlet.linux-arm buildlet.linux-arm-arm5 buildlet.linux-arm64 buildlet.linux-mips buildlet.linux-mipsle buildlet.linux-mips64 buildlet.linux-mips64le buildlet.linux-ppc64 buildlet.linux-ppc64le buildlet.linux-s390x buildlet.solaris-amd64
	echo "done"

dev-buildlet.linux-arm: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false dev-go-builder-data/buildlet.linux-arm

dev-buildlet.linux-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false dev-go-builder-data/buildlet.linux-amd64

dev-buildlet.windows-amd64: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public --cacheable=false dev-go-builder-data/buildlet.windows-amd64

dev-buildlet.plan9-386: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=$@ --file=go:golang.org/x/build/cmd/buildlet --public dev-go-builder-data/buildlet.plan9-386
