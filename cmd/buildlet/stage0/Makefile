buildlet-stage0.windows-amd64: stage0.go
	GOOS=windows GOARCH=amd64 go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)

buildlet-stage0.linux-arm-scaleway: stage0.go
	GOOS=linux GOARCH=arm go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)
