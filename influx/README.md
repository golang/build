# InfluxDB container image

This directory contains the source for the InfluxDB container image used in the
Go Performance Monitoring system. The image is based on the Google-maintained
GCP InfluxDB 2 image, with an additional small program to perform initial
database setup and push access credentials to Google Secret Manager.

## Local

To run an instance locally:

    $ sudo docker build -t golang_influx . && sudo docker run --rm -p 443:8086 golang_influx

Browse / API connect to https://localhost:8086 (note that the instance uses a
self-signed certificate), and authenticate with user 'admin' or 'reader' with
the password or API token logged by the container.
