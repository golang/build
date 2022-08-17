# golang.org/x/build/cmd/securitybot

securitybot provides TryBot-like functionality for the internal private Go
repository that is used for developing patches for security releases.

securitybot is not nearly as fully featured as the public TryBot functionality,
and is meant to be a best effort attempt at providing basic testing for security
patches.

securitybot operates in a loop, searching the private Gerrit instance for CLs
which have the `Run-TryBot+1` label, and are lacking either the
`TryBot-Result+1` or `TryBot-Result-1` labels. It then executes the tests for
each CL it finds serially. Since there is a low volume of security patches, it
is not necessary to run tests for each CL in parallel. securitybot is not
intended to be able to run concurrently.

Tests for each CL are executed by creating buildlets for each configured builder
(currently just those that represent the first class ports) and executing the
`all.{bash,bat}` script. Logs are stored in a GCS bucket, and updated every 5s
while the tests are running.

## Deploying

Deploying a new version of `securitybot` can be done as follows:

```
docker build -f Dockerfile -t golang/security-trybots ../..
docker tag golang/security-trybots gcr.io/go-security-trybots/security-trybots
docker push gcr.io/go-security-trybots/security-trybots
kubectl rollout restart -f deployment.yaml
```

## Setting up cluster

The cluster and service accounts have already been setup and configured, but in
case this needs to be done again, the following commands were used. The second
command binds the Kuberenetes service account (defined in `deployment.yaml`) to
the GCP service account.

```
gcloud container \
  --project "go-security-trybots" \
  clusters create-auto "trybots" \
  --region "us-central1" \
  --release-channel "regular" \
  --network "projects/go-security-trybots/global/networks/default" \
  --subnetwork "projects/go-security-trybots/regions/us-central1/subnetworks/default" \
  --cluster-ipv4-cidr "/17" \
  --services-ipv4-cidr "/22"

gcloud iam service-accounts add-iam-policy-binding \
  --role roles/iam.workloadIdentityUser \
  --member "serviceAccount:go-security-trybots.svc.id.goog[default/security-trybots]" \
  security-trybots@go-security-trybots.iam.gserviceaccount.com
```