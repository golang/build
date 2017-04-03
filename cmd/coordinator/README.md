# Coordinator

## Running locally

Run

    go install golang.org/x/build/cmd/coordinator && coordinator --mode=dev

to start a server on https://localhost:8119. Proceed past the TLS warning and
you should get the homepage. Some features won't work when running locally,
but you should be able to view the homepage and the builders page and do basic
sanity checks.
