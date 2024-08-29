Subject: Gopls {{.Version}} is released

Hello gophers,

We have just released Gopls {{.Version}}, a release candidate for the upcoming gopls release. It is picked from release branch {{.Branch}} at commit {{.Commit | shortcommit}}.

If you have Go installed already, an easy way to try {{.Version}} is by using the go command:

```
$ go install golang.org/x/tools/gopls@{{.Version}}
```

This gopls release is being tracked at https://go.dev/issue/{{.Issue}}.

Cheers,
The Go Tools Team
