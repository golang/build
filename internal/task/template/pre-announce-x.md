Subject: [security] {{.Module}} fix pre-announcement

Hello gophers,

We plan to issue a security fix for the package{{$numPkgs := len .Packages}}{{if gt $numPkgs 1}}s{{end}} {{join .Packages}} in the {{.Module}} module during US business hours on {{.Target.Format "Monday, January 2"}}.

This will cover the following CVEs:

{{range .CVEs}}- {{.}}
{{end}}

Following our security policy, this is the pre-announcement of the fix.

Thanks,
{{with .Names}}{{join .}} for the{{else}}The{{end}} Go team
