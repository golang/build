Subject: Go {{.Version|major}} Release Candidate {{.Version|build}} is released

Hello gophers,

We have just released {{.Version}}, a release candidate version of Go {{.Version|major}}.
It is cut from release-branch.go{{.Version|major}} at the revision tagged {{.Version}}.

Please try your production load tests and unit tests with the new version.
Your help testing these pre-release versions is invaluable.

Report any problems using the issue tracker:
https://go.dev/issue/new

If you have Go installed already, an easy way to try {{.Version}}
is by using the go command:

$ go install golang.org/dl/{{.Version}}@latest
$ {{.Version}} download

You can download binary and source distributions from the usual place:
https://go.dev/dl/#{{.Version}}

{{ if atLeast .Version "go1.23" }}
To help validate the release, consider opting in to [Go toolchain telemetry](https://go.dev/doc/telemetry).
If you're using go command version go1.23rc1 or later, you can opt in by
running the following command:

$ go telemetry on

{{ end -}}

To find out what has changed in Go {{.Version|major}}, read the draft release notes:
https://tip.golang.org/doc/go{{.Version|major}}

Cheers,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
