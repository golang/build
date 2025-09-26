# Deploying services

Services in x/build that are deployed to our GKE cluster all follow the same
workflow.
In the directories containing their `main` package should also be a Makefile
that follows the process described below.

### First-time setup

Install the Google Cloud CLI. See [instructions](https://cloud.google.com/sdk/docs/install-sdk).
You should have `gcloud` and `kubectl` available in PATH.

### Prod

First, configure `gcloud` to target our production environment:

```sh
$ gcloud config set project symbolic-datum-552
$ gcloud container clusters get-credentials --zone=us-central1 services
```

Afterwards, each time you wish to deploy a service, cd into its directory and run:

```sh
$ make deploy-prod
```
