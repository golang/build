# devapp

## Local development

```sh
$ go run . -listen-http=localhost:8080
```

Then visit http://localhost:8080/ in your browser.

## Deployment

See the documentation on [deployment](../doc/deployment.md).

Note that devapp files for deployment have already been moved
to the cmd/devapp directory, but the command hasn't moved yet.
Use the cmd/devapp directory when running deployment commands.
