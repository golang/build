buildlet: buildlet.go reverse.go buildlet_windows.go
	go build

buildlet.darwin-amd64: buildlet.go reverse.go
	GOOS=darwin GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.freebsd-amd64: buildlet.go reverse.go
	GOOS=freebsd GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.linux-amd64: buildlet.go reverse.go
	GOOS=linux GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.netbsd-amd64: buildlet.go reverse.go
	GOOS=netbsd GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.openbsd-amd64: buildlet.go reverse.go
	GOOS=openbsd GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.openbsd-386: buildlet.go reverse.go
	GOOS=openbsd GOARCH=386 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.plan9-386: buildlet.go reverse.go
	GOOS=plan9 GOARCH=386 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.windows-amd64: buildlet.go reverse.go buildlet_windows.go
	GOOS=windows GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

buildlet.linux-arm: buildlet.go reverse.go
	GOOS=linux GOARCH=arm go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public go-builder-data/$@)

dev-buildlet.linux-arm: buildlet.go reverse.go
	GOOS=linux GOARCH=arm go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public dev-go-builder-data/buildlet.linux-arm)

dev-buildlet.linux-amd64: buildlet.go reverse.go
	GOOS=linux GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public dev-go-builder-data/buildlet.linux-amd64)

dev-buildlet.windows-amd64: buildlet.go reverse.go buildlet_windows.go
	GOOS=windows GOARCH=amd64 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public dev-go-builder-data/buildlet.windows-amd64)

dev-buildlet.plan9-386: buildlet.go reverse.go
	GOOS=plan9 GOARCH=386 go build -o $@
	cat $@ | (cd ../upload && go run upload.go --public dev-go-builder-data/buildlet.plan9-386)

