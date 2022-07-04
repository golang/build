# Coordinator

Building, running tests, running locally is supported on Linux and macOS only.

## Running locally in dev mode

```sh
go run . -mode=dev -listen-http=localhost:8080
```

Then visit http://localhost:8080/ in your browser.

Some features won't work when running in dev mode,
but you should be able to navigate between the homepage, the build dashboard,
the builders page, and do limited local development and testing.

To test builds locally, start a `host-linux-amd64-localdev` reverse buildlet,
which will run `linux-amd64` tests:

```sh
go run golang.org/x/build/cmd/buildlet -halt=false -reverse-type=host-linux-amd64-localdev
```

To view/modify the "Trybot Status" page locally, visit the /try-dev endpoint.
You should see a trybot status page with some example data.
