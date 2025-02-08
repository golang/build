## {{ .Current }} {{- if .IsPrerelease }} (prerelease) {{- end }}

Date: {{ .Date }}

{{ if .IsPrerelease -}}

This is the [pre-release version](https://code.visualstudio.com/api/working-with-extensions/publishing-extension#prerelease-extensions) of {{ .NextStable }}.

{{ end -}}

**Full Changelog**: https://github.com/golang/vscode-go/compare/{{ .Previous }}...{{ .Current }}
**Milestone**: https://github.com/golang/vscode-go/issues?q=milestone%3A{{ .Milestone }}

