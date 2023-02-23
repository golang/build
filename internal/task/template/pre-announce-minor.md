Subject: [security] Go {{short .Version}}{{with .SecondaryVersion}} and Go {{. | short}}{{end}} pre-announcement

Hello gophers,

We plan to issue Go {{short .Version}}{{with .SecondaryVersion}} and Go {{. | short}}{{end}} during US business hours on {{.Target.Format "Monday, January 2"}}.

{{if .SecondaryVersion -}}
These minor releases include
{{- else -}}
This minor release includes{{end}} PRIVATE security fixes to {{.Security}}, covering the following CVE{{if gt (len .CVEs) 1}}s{{end}}:

{{range .CVEs}}- {{.}}
{{end}}

Following our security policy, this is the pre-announcement of {{if .SecondaryVersion}}those releases{{else}}the release{{end}}.

Thanks,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
