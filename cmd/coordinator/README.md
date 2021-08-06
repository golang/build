# Coordinator

## Running locally

Building, running tests, running locally is supported on Linux and macOS only.

Run

```sh
go run golang.org/x/build/cmd/coordinator -mode=dev -env=dev
```

to start a server on https://localhost:8119. Proceed past the TLS warning and
you should get the homepage. Some features won't work when running locally,
but you should be able to navigate between the homepage, the dashboard,
the builders page, and do limited local development and testing.

#### Render the "Trybot Status" page locally

To view/modify the "Trybot Status" page locally, you can run the coordinator
with the `-dev` tag.

```sh
go run -tags=dev golang.org/x/build/cmd/coordinator -mode=dev -env=dev
```

Then visit https://localhost:8119/try-dev in your browser.
You should see a trybot status page with some example data.
