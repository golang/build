# golang.org/x/build/cmd/pubsubhelper

## Running with Docker locally

```sh
docker run --rm -it \
    -p 80:80 \
    -p 25:25 \
    -p 443:443 \
    gcr.io/go-dashboard-dev/pubsubhelper:latest [any additional pubsubhelper flags]
```

## Deployment

See the documentation on [deployment](../../doc/deployment.md).
