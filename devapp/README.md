# devapp

## Local development

```sh
$ go build
$ ./devapp
```

Then visit http://localhost:6343

## Deployment

```sh
$ gcloud config set project {go-dashboard-dev|symbolic-datum-552}
$ gcloud container clusters get-credentials --zone=us-central1-f go
$ make push-{dev|prod}
$ kubectl create -f deployment-{dev|prod}.yaml
$ kubectl create -f service-{dev|prod}.yaml
```