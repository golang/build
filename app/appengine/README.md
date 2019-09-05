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
GO111MODULE=on gcloud app deploy app.yaml
```

or, to not affect your gcloud state, use:

```
GO111MODULE=on gcloud app --account=username@google.com --project=golang-org deploy app.yaml
```
