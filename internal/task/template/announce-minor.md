Subject: {{subjectPrefix .}}
	Go {{short .Version}} {{with .SecondaryVersion}}and Go {{. | short}} are{{else}}is{{end}} released

Hello gophers,

{{if .SecondaryVersion -}}
We have just released Go versions {{short .Version}} and {{short .SecondaryVersion}}, minor point releases.
{{- else -}}
We have just released Go version {{short .Version}}, a minor point release.
{{- end}}

{{if .Security}}{{if .SecondaryVersion -}}
These minor releases include
{{- else -}}
This minor release includes{{end}} {{len .Security}} security fixes following the [security policy](https://go.dev/security):
{{range .Security}}
-{{indent .}}
{{end}}
{{end -}}

View the release notes for more information:
https://go.dev/doc/devel/release#{{.Version}}

You can download binary and source distributions from the Go website:
https://go.dev/dl/

To compile from source using a Git clone, update to the release with
`git checkout {{.Version}}` and build as usual.

Thanks to everyone who contributed to the release{{if .SecondaryVersion}}s{{end}}.

Cheers,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
