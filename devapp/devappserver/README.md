# devappserver

## Local development

```sh
$ make devappserver
$ ./devappserver -http=:8080
```

Then visit http://localhost:8080

## Deployment

```sh
$ gcloud config set project {go-dashboard-dev|symbolic-datum-552}
$ gcloud container clusters get-credentials --zone=us-central1-f go
$ make push-{dev|prod}
$ kubectl create -f service.yaml
$ kubectl create -f deployment-prod.yaml
```