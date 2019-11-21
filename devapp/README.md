# devapp

## Local development

```sh
$ go build
$ ./devapp
```

Then visit http://localhost:6343

## Deployment

### First-time setup

Install the `docker`, `kubectl`, and `gcloud` utilities.

Verify that `docker run hello-world` works without `sudo`. (You may need to run
`sudo adduser $USER docker`, then either log out and back in or run `newgrp
docker`.)

Then run:

```sh
$ gcloud auth configure-docker
```

Install the App Engine Go SDK: [instructions](https://cloud.google.com/sdk/docs/quickstart-debian-ubuntu)

### Staging

```sh
$ gcloud config set project go-dashboard-dev
$ gcloud container clusters get-credentials --zone=us-central1-f go
$ make push-staging
```

If creating the deployment and service the first time:

```sh
$ kubectl create -f deployment-staging.yaml
$ kubectl create -f service-staging.yaml
```

If updating the pod image:

```sh
$ make deploy-staging
```

### Prod

```sh
$ gcloud config set project symbolic-datum-552
$ gcloud container clusters get-credentials --zone=us-central1-f go
$ make push-prod
```

If creating the deployment and service the first time:

```sh
$ kubectl create -f deployment-prod.yaml
$ kubectl create -f service-prod.yaml
```

If updating the pod image:

```sh
$ make deploy-prod
```
