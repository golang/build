Subject: [security] Vulnerabilit{{if gt (len .Security) 1}}ies{{else}}y{{end}} in {{.Module}}

Hello gophers,

We have tagged version {{.Version}} of {{.Module}} in order to address {{if gt (len .Security) 1}}the following security issues{{else}}a security issue{{end}}:
{{range .Security}}
-{{indent .}}
{{end}}
Cheers,
Go Security team
