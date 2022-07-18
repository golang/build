# vcs-test

We run a version control server for testing at `vcs-test.golang.org`.

## Repositories

The server can serve Bazaar, Fossil, Git, Mercurial, and Subversion repositories.
The root of each repository is `https://vcs-test.golang.org/VCS/REPONAME`,
where `VCS` is the version control system's command name (`bzr` for Bazaar, and so on),
and `REPONAME` is the repository name.

To serve a particular repository, the server downloads
`gs://vcs-test/VCS/REPONAME.zip` from Google Cloud Storage and unzips it
into an empty directory.
The result should be a valid repository directory for the given version control system.
If the needed format of the zip file is unclear, download and inspect `gs://vcs-test/VCS/hello.zip`
from `https://vcs-test.storage.googleapis.com/VCS/hello.zip`.

Google Cloud Storage imposes a default `Cache-Control` policy of 3600 seconds for
publicly-readable objects; for instructions to disable caching per object, see
[`gsutil setmeta`](https://cloud.google.com/storage/docs/gsutil/commands/setmeta).
`vcweb` itself may serve stale data for up to five minutes after a zip file is updated.
To force a rescan of Google Cloud Storage, fetch
`https://vcs-test.golang.org/VCS/REPONAME?vcweb-force-reload=1`.

## Static files

The URL space `https://vcs-test.golang.org/go/NAME` is served by static files,
fetched from `gs://vcs-test/go/NAME.zip`.
The main use for static files is to write redirect HTML.
See `gs://vcs-test/go/hello.zip` for examples.
Note that because the server uses `http.DetectContentType` to deduce
the content type from file data, it is not necessary to
name HTML files with a `.html` suffix.

## HTTPS

The server fetches an HTTPS certificate on demand from Let's Encrypt,
using `golang.org/x/crypto/acme/autocert`.
It caches the certificates in `gs://vcs-test-autocert` using
`golang.org/x/build/autocertcache`.

