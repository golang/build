FROM golang:1.12 AS build

ENV GO111MODULE=on
ENV GOPROXY=https://proxy.golang.org

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Optimization for iterative docker build speed, not necessary for correctness:
# TODO: write a tool to make writing Go module-friendly Dockerfiles easier.
RUN go install golang.org/x/crypto/acme/autocert
RUN go install cloud.google.com/go/storage
COPY autocertcache /go/src/golang.org/x/build/autocertcache
RUN go install golang.org/x/build/autocertcache

COPY . /go/src/golang.org/x/build/
# Install binary to /go/bin:
RUN go install golang.org/x/build/cmd/tip

FROM golang:1.12

RUN apt-get update && apt-get install --no-install-recommends -y -q build-essential git
# golang puts its go install here (weird but true)
ENV GOROOT_BOOTSTRAP /usr/local/go

# We listen on 8080 (for historical reasons). The service.yaml maps public port 80 to 8080.
# We also listen on 443 for LetsEncrypt TLS.
EXPOSE 8080 443

COPY --from=build /go/bin/tip /go/bin/tip
ENTRYPOINT ["/go/bin/tip"]
