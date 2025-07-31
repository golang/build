Subject: {{subjectPrefix .}}
	Go {{.Version|major}} Release Candidate {{.Version|build}} is released

Hello gophers,

We have just released {{.Version}}, a release candidate version of Go {{.Version|major}}.
It is cut from release-branch.go{{.Version|major}} at the revision tagged {{.Version}}.

{{if .Security}}This release includes {{len .Security}} security fix{{if gt (len .Security) 1}}es{{end}} following the [security policy](https://go.dev/doc/security/policy):
{{range .Security}}
-{{indent .}}
{{end}}
{{end -}}

Please try your production load tests and unit tests with the new version.
Your help testing these pre-release versions is invaluable.

Report any problems using the issue tracker:
https://go.dev/issue/new

If you have Go installed already, an easy way to try {{.Version}}
is by using the go command:

```
$ go install golang.org/dl/{{.Version}}@latest
$ {{.Version}} download
```

You can download binary and source distributions from the usual place:
https://go.dev/dl/#{{.Version}}

To find out what has changed in Go {{.Version|major}}, read the draft release notes:
https://tip.golang.org/doc/go{{.Version|major}}

Cheers,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
