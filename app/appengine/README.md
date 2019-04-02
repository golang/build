# build.golang.org App Engine App

This is the code that runs https://build.golang.org/

## Local development

On a machine with a browser:

```
dev_appserver.py --port=8080 .
```

With a remote VM with a port open to the Internet:

```
dev_appserver.py --enable_host_checking=false --host=0.0.0.0 --port=8080 .
```

## Deploying

```sh
gcloud config set project golang-org
gcloud app deploy --no-promote -v {build|build-test} app.yaml
```

or, to not affect your gcloud state, use:

```
gcloud app --account=username@google.com --project=golang-org deploy --no-promote -v build app.yaml
```

Using -v build will run as build.golang.org.
Using -v build-test will run as build-test.golang.org.
