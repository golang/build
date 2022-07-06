# InfluxDB container image

This directory contains the source for the InfluxDB container image used in the
Go Performance Monitoring system. The image is based on the Google-maintained
GCP InfluxDB 2 image, with an additional small program to perform initial
database setup and push access credentials to Google Secret Manager.

## Local

To run an instance locally:

    $ make docker-prod
    $ docker run --rm -p 443:8086 gcr.io/symbolic-datum-552/influx:latest

Browse / API connect to https://localhost:8086 (note that the instance uses a
self-signed certificate), and authenticate with user 'admin' or 'reader' with
the password or API token logged by the container.

## Google Cloud

One-time setup:

1. IAM setup, based on
   https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#authenticating_to:

  a. Create GCP service account:

    $ gcloud iam service-accounts create influx \
        --description="Runs golang.org/x/build/influx"

  c. Allow Kubernetes service account (created by deployment-prod.yaml) to
     impersonate the GCP service account:

    $ gcloud iam service-accounts add-iam-policy-binding \
        influx@<PROJECT>.iam.gserviceaccount.com \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:<PROJECT>.svc.id.goog[prod/influx]"

2. Secret Manager set up:

  a. Create the secrets to store InfluxDB passwords/tokens in:

    $ gcloud secrets create influx-admin-pass
    $ gcloud secrets create influx-admin-token
    $ gcloud secrets create influx-reader-pass
    $ gcloud secrets create influx-reader-token

  b. Grant access to the GCP service account to update the secrets.

    $ gcloud secrets add-iam-policy-binding influx-admin-pass --member=serviceAccount:influx@<PROJECT>.iam.gserviceaccount.com --role="roles/secretmanager.secretVersionAdder"
    $ gcloud secrets add-iam-policy-binding influx-admin-token --member=serviceAccount:influx@<PROJECT>.iam.gserviceaccount.com --role="roles/secretmanager.secretVersionAdder"
    $ gcloud secrets add-iam-policy-binding influx-reader-pass --member=serviceAccount:influx@<PROJECT>.iam.gserviceaccount.com --role="roles/secretmanager.secretVersionAdder"
    $ gcloud secrets add-iam-policy-binding influx-reader-token --member=serviceAccount:influx@<PROJECT>.iam.gserviceaccount.com --role="roles/secretmanager.secretVersionAdder"

### Accessing Influx

The available users on Influx are 'admin' (full access) and 'reader'
(read-only). To login as 'reader', use the following to access the password:

  $ gcloud --project=symbolic-datum-552 secrets versions access latest --secret=influx-reader-pass

Then login at https://influx.golang.org.

To access the admin password, admin API token, or reader API token, change to
`--secret` to one of `influx-admin-pass`, `influx-admin-token`, or
`influx-reader-token`, respectively.

## Deployment

See the documentation on [deployment](../doc/deployment.md).
