# Common targets:
#
#  - compile: build all buildlets (but do not upload)
#  - upload: build and upload all buildlets to prod
#  - dev-upload: build and upload all buildlets to dev cluster
#  - install-go: install the version of Go needed by the Makefile
#
# Individual targets:
#
#  - buildlet.linux-amd64: build buildlet for linux-amd64 (but do not upload)
#  - upload.linux-amd64: build buildlet for linux-amd64 and upload to prod
#  - dev-upload.linux-amd64: build buildlet for linux-amd64 and upload to dev cluster

GO=go1.22.8

ALL=\
	buildlet.aix-ppc64 \
	buildlet.darwin-amd64 \
	buildlet.darwin-arm64 \
	buildlet.dragonfly-amd64 \
	buildlet.freebsd-amd64 \
	buildlet.freebsd-arm \
	buildlet.freebsd-arm-5 \
	buildlet.freebsd-arm-7 \
	buildlet.freebsd-riscv64 \
	buildlet.illumos-amd64 \
	buildlet.linux-amd64 \
	buildlet.linux-arm \
	buildlet.linux-arm-5 \
	buildlet.linux-arm-7 \
	buildlet.linux-arm-arm5 \
	buildlet.linux-arm64 \
	buildlet.linux-mips \
	buildlet.linux-mips64 \
	buildlet.linux-mips64le \
	buildlet.linux-mipsle \
	buildlet.linux-ppc64 \
	buildlet.linux-ppc64le \
	buildlet.linux-riscv64 \
	buildlet.linux-s390x \
	buildlet.netbsd-386 \
	buildlet.netbsd-amd64 \
	buildlet.netbsd-arm \
	buildlet.netbsd-arm-5 \
	buildlet.netbsd-arm-7 \
	buildlet.netbsd-arm64 \
	buildlet.openbsd-386 \
	buildlet.openbsd-amd64 \
	buildlet.openbsd-arm \
	buildlet.openbsd-arm-7 \
	buildlet.plan9-386 \
	buildlet.plan9-amd64 \
	buildlet.plan9-arm \
	buildlet.plan9-arm-5 \
	buildlet.plan9-arm-7 \
	buildlet.solaris-amd64 \
	buildlet.windows-386 \
	buildlet.windows-amd64 \
	buildlet.windows-arm64 \

usage: FORCE
	@sed -E '/^$$/q; s/^# ?//' Makefile
	exit 1

FORCE:

# A convenience for people who are missing $(GO).
install-go:
	go install golang.org/dl/$(GO)@latest
	$(GO) download

clean:
	rm -f buildlet.*-*

# Compile everything without uploading.
compile: $(ALL)

GOFILES:=$(shell ls *.go)

VERSION:=$(USER)-$(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

BUILD=CGO_ENABLED=0 $(GO) build -ldflags='-X main.Version=$(VERSION)' -o

buildlet.%-386: $(GOFILES)
	GOOS=$* GOARCH=386 $(BUILD) $@

buildlet.%-amd64: $(GOFILES)
	GOOS=$* GOARCH=amd64 $(BUILD) $@

# For ARM we need both GOARM settings because on some operating systems
# (for example OpenBSD) we can't write a single binary that runs on both ARMv5 and ARMv7.
buildlet.%-arm-5: $(GOFILES)
	GOOS=$* GOARCH=arm GOARM=5 $(BUILD) $@
buildlet.%-arm-7: $(GOFILES)
	GOOS=$* GOARCH=arm GOARM=7 $(BUILD) $@

# Legacy names that existing reverse builders may still be downloading.
# Yes, both the Linux variants are GOARM=5.
# (On Linux it doesn't matter much since we use the vdso routines for atomics.)
# Reverse builders should eventually be updated to use the -5 or -7 form directly.
buildlet.freebsd-arm: buildlet.freebsd-arm-5
	cp $< $@
buildlet.linux-arm: buildlet.linux-arm-5
	cp $< $@
buildlet.linux-arm-arm5: buildlet.linux-arm-5
	cp $< $@
buildlet.netbsd-arm: buildlet.netbsd-arm-7
	cp $< $@
buildlet.openbsd-arm: buildlet.openbsd-arm-7
	cp $< $@
buildlet.plan9-arm: buildlet.plan9-arm-5
	cp $< $@

buildlet.%-arm64: $(GOFILES)
	GOOS=$* GOARCH=arm64 $(BUILD) $@

buildlet.%-mips: $(GOFILES)
	GOOS=$* GOARCH=mips $(BUILD) $@

buildlet.%-mipsle: $(GOFILES)
	GOOS=$* GOARCH=mipsle $(BUILD) $@

buildlet.%-mips64: $(GOFILES)
	GOOS=$* GOARCH=mips64 $(BUILD) $@

buildlet.%-mips64le: $(GOFILES)
	GOOS=$* GOARCH=mips64le $(BUILD) $@

buildlet.%-ppc64: $(GOFILES)
	GOOS=$* GOARCH=ppc64 $(BUILD) $@

buildlet.%-ppc64le: $(GOFILES)
	GOOS=$* GOARCH=ppc64le $(BUILD) $@

buildlet.%-riscv64: $(GOFILES)
	GOOS=$* GOARCH=riscv64 $(BUILD) $@

buildlet.%-s390x: $(GOFILES)
	GOOS=$* GOARCH=s390x $(BUILD) $@

# Upload everything.
upload: $(ALL:buildlet.%=upload.%)

UPLOAD=$(GO) run golang.org/x/build/cmd/upload -verbose -public -cacheable=false

upload.%: buildlet.%
	$(UPLOAD) -file=$< go-builder-data/$<

# Upload everything to dev cluster.
dev-upload: $(ALL:buildlet.%=upload.%)

dev-upload.%: buildlet.%
	$(UPLOAD) -file=$< dev-go-builder-data/$<

