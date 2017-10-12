# build.golang.org App Engine App

Update with

```sh
gcloud config set project golang-org
gcloud app deploy --no-promote -v {build|build-test} app.yaml
```

Using -v build will run as build.golang.org.
Using -v build-test will run as build-test.golang.org.
