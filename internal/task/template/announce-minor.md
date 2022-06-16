Subject: {{subjectPrefix .}}
	Go {{short .Version}} and Go {{short .SecondaryVersion}} are released

Hello gophers,

We have just released Go versions {{short .Version}} and {{short .SecondaryVersion}}, minor point releases.

{{with .Security}}These minor releases include {{len .}} security fixes following the [security policy](https://go.dev/security):
{{range .}}
-{{indent .}}
{{end}}
{{end -}}

View the release notes for more information:
https://go.dev/doc/devel/release#{{.Version}}

You can download binary and source distributions from the Go website:
https://go.dev/dl/

To compile from source using a Git clone, update to the release with
`git checkout {{.Version}}` and build as usual.

Thanks to everyone who contributed to the releases.

Cheers,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
