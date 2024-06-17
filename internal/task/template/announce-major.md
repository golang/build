Subject: Go {{short .Version}} is released

Hello gophers,

We have just released Go {{short .Version}}.

To find out what has changed in Go {{major .Version}}, read the release notes:
https://go.dev/doc/go{{.Version|major}}

You can download binary and source distributions from our download page:
https://go.dev/dl/#{{.Version}}

If you have Go installed already, an easy way to try {{.Version}}
is by using the go command:

```
$ go install golang.org/dl/{{.Version}}@latest
$ {{.Version}} download
```

To compile from source using a Git clone, update to the release with
`git checkout {{.Version}}` and build as usual.

Thanks to everyone who contributed to the release!

Cheers,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
