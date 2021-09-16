# golang.org/x/build/cmd/relui

```
               ▀▀█             ▀
  ▄ ▄▄   ▄▄▄     █    ▄   ▄  ▄▄▄
  █▀  ▀ █▀  █    █    █   █    █
  █     █▀▀▀▀    █    █   █    █
  █     ▀█▄▄▀    ▀▄▄  ▀▄▄▀█  ▄▄█▄▄
```

relui is a web interface for managing the release process of Go.

## Development

Run the command with the appropriate
[libpq-style environment variables](https://www.postgresql.org/docs/current/libpq-envars.html)
set.

```bash
PGHOST=localhost PGDATABASE=relui-dev PGUSER=postgres go run ./
```

Alternatively, using docker:

```bash
make dev
```

### Updating Queries

Create or edit SQL files in `internal/relui/queries`.
After editing the query, run `sqlc generate` in this directory. The
`internal/relui/db` package contains the generated code.

See [sqlc documentation](https://docs.sqlc.dev/en/stable/) for further
details.

## Testing

Run go test with the appropriate
[libpq-style environment variables](https://www.postgresql.org/docs/current/libpq-envars.html)
set. If the database connection fails, database integration tests will
be skipped. If PGDATABSE is unset, relui-test is used by default.

```bash
PGHOST=localhost PGUSER=postgres go test -v ./... ../../internal/relui/...
```

Alternatively, using docker:
```bash
make test
```
