# build.golang.org App Engine App

This is the code that runs https://build.golang.org/

## Local development

To use production maintner data (for the GetDashboard RPC containing
the list of commits, etc) and production active builds (from the
coordinator), both of which are open to anybody, use:

```
go run . --dev --fake-results
```

If you also want to use the production datastore for real commit data,
or you want to work on the handlers that mutate data in the datastore,
use:

```
go run . --dev
```

That requires access to the "golang-org" GCP project's datastore.

Environment variables you can change:

* `PORT`: plain port number or Go-style listen address
* `DATASTORE_PROJECT_ID`: defaults to `"golang-org"` in dev mode
* `MAINTNER_ADDR`: defaults to "maintner.golang.org"

## Deploying a test version

To deploy to the production project but to a version that's not promoted to the default URL:

```sh
make deploy-test
```

It will tell you what URL it deployed to. You can then check it and
either delete it or promote it with either the gcloud or web UIs. Or
just ignore it. They'll scale to zero and are only visual clutter
until somebody deletes a batch of old ones.

## Deploying to production

To deploy to https://build.golang.org:

```sh
make deploy-prod
```

