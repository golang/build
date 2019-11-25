usage:
	echo "See Makefile for usage"
	exit 1

deploy-prod:
	go install golang.org/x/build/cmd/xb
	GO111MODULE=on gcloud app --account=$$(xb google-email) --project=golang-org deploy app.yaml

deploy-test:
	go install golang.org/x/build/cmd/xb
	GO111MODULE=on gcloud app --account=$$(xb google-email) --project=golang-org deploy --no-promote app.yaml
