buildlet-stage0.windows-amd64: stage0.go
	GOOS=windows GOARCH=amd64 go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)

buildlet-stage0.linux-arm-scaleway: stage0.go
	GOOS=linux GOARCH=arm go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)

# No staging vs prod targets here, because this file gets baked into
# the Docker layers on gcr.io (which do have staging vs prod names).
# That is, this file on GCS is only used at "docker build"-time, not
# during day-to-day builds.
buildlet-stage0.linux-amd64-kube: stage0.go
	GOOS=linux GOARCH=amd64 go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)

buildlet-stage0.linux-s390x: stage0.go
	GOOS=linux GOARCH=s390x go build -o $@
	cat $@ | (cd ../../upload && go run upload.go --public --cacheable=false go-builder-data/$@)
