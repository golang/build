usage: FORCE
	# See Makefile targets.
	exit 1

FORCE:

upload: FORCE
	go install golang.org/x/build/cmd/upload
	gzip -c run-builder-darwin-10_8.sh > run.tmp.gz
	upload --verbose --file=run.tmp.gz -cacheable=false -public=false go-builder-data/run-builder-darwin-10_8.gz
