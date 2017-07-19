# Go Build Tools

This subrepository holds the source for various packages and tools that support
Go's build system and the development of the Go programming language.

## Download/Install

The easiest way to download this package is to run `go get -u
golang.org/x/build/...`. You can also manually git clone the repository to
`$GOPATH/src/golang.org/x/build`.

## Report Issues / Send Patches

This repository uses Gerrit for code changes. To learn how to submit changes to
this repository, see https://golang.org/doc/contribute.html.

The main issue tracker for the blog is located at
https://github.com/golang/go/issues. Prefix your issue with "x/build:" in the
subject line, so it is easy to find.

### Code Layout

```
app/: the App Engine code that runs https://build.golang.org/ and
      stores which builds have passed or failed. It is responsible for
      knowing which post-submit builds still need to be done. (It doesn't know
      anyting about trybot builds that need to be done)
      It doesn't execute any builds itself. See the coordinator.

buildenv/: variables with details of the production environment vs the
           staging environment.

cmd/:

  buildlet/: HTTP server that runs on a VM and is told what to write to disk
           and what command to run. This is cross-compiled to different architectures
           and is the first program run when a builder VM comes up. It then
           is contacted by the coordinator to do a build. Not all builders use
           the buildlet (at least not yet).

  builder/: gobuilder, a Go continuous build client. The original Go builder program.

  coordinator/: daemon that runs on CoreOS on Google Compute Engine and manages
          builds using Docker containers and/or VMs as needed.

  retrybuilds/: a Go client program to delete build results from the dashboard (app)

  upload/:  a Go program to upload to Google Cloud Storage. used by Makefiles elsewhere.

  gitmirror/: a daemon that watches for new commits to the Go repository and
              its sub-repositories, and notifies the dashboard of those commits,
              as well as syncing them to GitHub. It also serves tarballs to
              the coordinator.

dashboard/: the configuration of the various build configs and host configs.

env/:     configuration files describing the environment of builders and related
          binaries.

types/:   a Go package contain common types used by other pieces.
```

### Adding a Go Builder

If you wish to run a Go builder, please email
[golang-dev@googlegroups.com](mailto:golang-dev@googlegroups.com) first. There
is documentation at https://golang.org/wiki/DashboardBuilders, but depending
on the type of builder, we may want to run it ourselves, after you prepare an
environment description (resulting in a VM image) of it. See the env directory.
