# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.18 AS build

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app

RUN go build -o run-influx golang.org/x/build/influx

FROM marketplace.gcr.io/google/influxdb2:latest

COPY --from=build /app/run-influx /run-influx

# For now, generate a self-signed cert to use. Not for production use!
RUN openssl req -x509 -nodes -newkey rsa:2048 \
  -keyout /etc/ssl/influxdb-selfsigned.key \
  -out /etc/ssl/influxdb-selfsigned.crt \
  -days 30 \
  -subj '/CN=localhost'

# Run our setup application in the background and the parent Influx entrypoint
# in the foreground.
ENTRYPOINT ["/bin/sh"]
CMD ["-c", "/run-influx & /docker-entrypoint.sh influxd --tls-cert=/etc/ssl/influxdb-selfsigned.crt --tls-key=/etc/ssl/influxdb-selfsigned.key --http-bind-address=:443"]
