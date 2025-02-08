# Deploying services

Services in x/build that are deployed to our GKE cluster all follow the same
workflow.
In the directories containing their `main` package should also be a Makefile
that follows the process described below.

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

### Prod

First, configure `gcloud`:

```sh
$ gcloud config set project symbolic-datum-552
$ gcloud container clusters get-credentials --zone=us-central1 services
```

Then to deploy, run:

```sh
$ make deploy-prod
```
