Subject: Gopls {{.Version}} is released

Hello gophers,

We have just released Gopls {{.Version}}, a release candidate for the upcoming Gopls release. It is picked from release branch {{.Branch}} at the commit {{.Commit}}.

If you have Go installed already, an easy way to try {{.Version}} is by using the go command:

```
$ go install golang.org/x/tools/gopls@{{.Version}}
```

This Gopls release is being tracked at golang/go#{{.Issue}}.

Cheers.
