This package contains the https://perf.golang.org/ benchmark result
analysis system. It serves as a front-end to the benchmark result
storage system at https://perfdata.golang.org/.

Both storage and analysis can be run locally; the following commands will run
the complete stack on your machine with an in-memory datastore.

```
go install golang.org/x/build/perfdata/localperfdata@latest
go install golang.org/x/build/perf/localperf@latest
localperfdata -addr=:8081 -view_url_base=http://localhost:8080/search?q=upload: &
localperf -addr=:8080 -storage=http://localhost:8081
```

The storage system is designed to have a standardized REST
API at https://perfdata.golang.org/, and we encourage additional analysis
tools to be written against the API. An example client can be found in the
[perfdata](https://pkg.go.dev/golang.org/x/build/perfdata) package.
