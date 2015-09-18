coordinator: builders.go coordinator.go dash.go debug.go gce.go remote.go reverse.go status.go watcher.go watcher_process.go
	GOBIN=. GOOS=linux go install --ldflags="-X main.Version=$$USER-$$(TZ=UTC date +%FT%T)Z" -x .

# After "make upload", either reboot the machine, or ssh to it and:
#   sudo systemctl restart gobuild.service
# And watch its logs with:
#   sudo journalctl -f -u gobuild.service
upload: coordinator
	cat coordinator | (cd ../upload && go run upload.go --public --cacheable=false go-builder-data/coordinator)
dev-upload: coordinator
	cat coordinator | (cd ../upload && go run upload.go --public --cacheable=false dev-go-builder-data/coordinator)
