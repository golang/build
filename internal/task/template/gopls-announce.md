Subject: Gopls {{.Version}} is released

Hello gophers,

We have just released Gopls {{.Version}}. It is picked from release branch {{.Branch}} at commit {{.Commit | shortcommit}}.

If you have Go installed already, an easy way to try {{.Version}} is by using the go command:

```
$ go install golang.org/x/tools/gopls@{{.Version}}
```

Thanks to everyone who contributed to the release.

Cheers,
The Go Tools Team
