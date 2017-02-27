upload-staging:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=freebsd-amd64-110.tar.gz --public dev-go-builder-data/freebsd-amd64-110.tar.gz

upload-prod:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=freebsd-amd64-110.tar.gz --public go-builder-data/freebsd-amd64-110.tar.gz

create-image-staging:
	gcloud compute --project go-dashboard-dev images create freebsd-amd64-110 --source-uri gs://dev-go-builder-data/freebsd-amd64-110.tar.gz

create-image-prod:
	gcloud compute --project symbolic-datum-552 images create freebsd-amd64-110 --source-uri gs://go-builder-data/freebsd-amd64-110.tar.gz

