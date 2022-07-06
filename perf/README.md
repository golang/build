# Performance analysis system

This package contains the https://perf.golang.org/ benchmark result analysis
system. It serves as a front-end to the benchmark result storage system at
https://perfdata.golang.org/.

The storage system is designed to have a standardized REST API at
https://perfdata.golang.org/, and we encourage additional analysis tools to be
written against the API. An example client can be found in the
[perfdata](https://pkg.go.dev/golang.org/x/build/perfdata) package.

## Local

Both storage and analysis can be run locally; the following commands will run
the complete stack on your machine with an in-memory datastore.

To run the storage system:

    $ go install golang.org/x/build/perfdata/localperfdata@latest
    $ localperfdata -addr=:8081 -view_url_base=http://localhost:8080/search?q=upload: &

To run the analysis frontend:

    $ make docker-prod
    $ docker run --rm --net=host gcr.io/symbolic-datum-552/perf:latest -listen-http=:8080 -perfdata=http://localhost:8081

Browse to https://localhost:8080 (note that the instance uses a self-signed
certificate).

## Google Cloud

One-time setup:

1. IAM setup, based on
   https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#authenticating_to:

  a. Create GCP service account:

    $ gcloud iam service-accounts create perf-prod \
        --description="Runs golang.org/x/build/perf"

  c. Allow Kubernetes service account (created by deployment-prod.yaml) to
     impersonate the GCP service account:

    $ gcloud iam service-accounts add-iam-policy-binding \
        perf-prod@<PROJECT>.iam.gserviceaccount.com \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:<PROJECT>.svc.id.goog[prod/perf-prod]"

## Deployment

See the documentation on [deployment](../doc/deployment.md).
