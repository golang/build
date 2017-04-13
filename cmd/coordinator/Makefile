usage: FORCE
	# See Makefile targets.
	exit 1

FORCE:

# After "make upload", either reboot the machine, or ssh to it and:
#   sudo systemctl restart gobuild.service
# And watch its logs with:
#   sudo journalctl -f -u gobuild.service
upload: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=linux-amd64 --file=go:golang.org/x/build/cmd/coordinator --public go-builder-data/coordinator

dev-upload: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=linux-amd64 --file=go:golang.org/x/build/cmd/coordinator --public dev-go-builder-data/coordinator

VERSION=v1
DOCKER_IMAGE_STAGE0=stage0/coordinator:latest
DOCKER_CTR_STAGE0=coordinator-stage0

stage0: *.go Dockerfile.0
	docker build --force-rm -f Dockerfile.0 --tag=$(DOCKER_IMAGE_STAGE0) ../..

coordinator: stage0
	docker create --name $(DOCKER_CTR_STAGE0) $(DOCKER_IMAGE_STAGE0)
	docker cp $(DOCKER_CTR_STAGE0):/go/bin/$@ $@
	docker rm $(DOCKER_CTR_STAGE0)

ca-certificates.crt:
	docker create --name $(DOCKER_CTR_STAGE0) $(DOCKER_IMAGE_STAGE0)
	docker cp $(DOCKER_CTR_STAGE0):/etc/ssl/certs/$@ $@
	docker rm $(DOCKER_CTR_STAGE0)

docker-prod: Dockerfile coordinator ca-certificates.crt
	docker build --force-rm --tag=gcr.io/symbolic-datum-552/coordinator:$(VERSION) .
docker-dev: Dockerfile coordinator ca-certificates.crt
	docker build --force-rm --tag=gcr.io/go-dashboard-dev/coordinator:latest .

push-prod: docker-prod
	gcloud docker -- push gcr.io/symbolic-datum-552/coordinator:$(VERSION)
push-dev: docker-dev
	gcloud docker -- push gcr.io/go-dashboard-dev/coordinator:latest

clean: FORCE
	$(RM) coordinator
	$(RM) ca-certificates.crt
