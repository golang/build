# TODO: move all this into the xb command and use it everywhere.
# For now it's expected that this is hand-tweaked per version bump.

upload-staging:
	go run golang.org/x/build/cmd/upload --verbose --file=freebsd-amd64-142.tar.gz --public dev-go-builder-data/freebsd-amd64-142.tar.gz

upload-prod:
	go run golang.org/x/build/cmd/upload --verbose --file=freebsd-amd64-142.tar.gz --public go-builder-data/freebsd-amd64-142.tar.gz

create-image-staging:
	gcloud compute --project go-dashboard-dev images create freebsd-amd64-142 --source-uri gs://dev-go-builder-data/freebsd-amd64-142.tar.gz

create-image-prod:
	gcloud compute --project symbolic-datum-552 images create freebsd-amd64-142 --source-uri gs://go-builder-data/freebsd-amd64-142.tar.gz

delete-image-prod:
	gcloud compute --project symbolic-datum-552 images delete freebsd-amd64-142
