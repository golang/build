upload-staging:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=netbsd-amd64-gce.tar.gz --public dev-go-builder-data/vm-image-netbsd-amd64-70.tar.gz

upload-prod:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=netbsd-amd64-gce.tar.gz --public go-builder-data/vm-image-netbsd-amd64-70.tar.gz

create-image-staging:
	gcloud compute --project go-dashboard-dev images create netbsd-amd64-70 --source-uri gs://dev-go-builder-data/vm-image-netbsd-amd64-70.tar.gz

create-image-prod:
	gcloud compute --project symbolic-datum-552 images create netbsd-amd64-70 --source-uri gs://go-builder-data/vm-image-netbsd-amd64-70.tar.gz

