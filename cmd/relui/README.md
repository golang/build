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

### Creating & Running Database Migrations

Migrations are managed using `github.com/golang-migrate/migrate`. 

#### Creating

```bash
go run -tags pgx github.com/golang-migrate/migrate/v4/cmd/migrate \
  create \
  --dir ../../internal/relui/migrations/ \
  -ext sql \
  my_fancy_migration
# alternatively, install the migrate command with pgx support.
```

#### Running

Migrations are automatically ran on application launch. Updating
schema.sql, as documented below, will also run migrations. "Down"
migrations are not automatically run and must be manually invoked in
`psql`, or by the `--migrate-down-up` flag or `make migrate-down-up`.

#### cmd/relui/schema.sql

`schema.sql` is updated by running the following command in this
directory:

```bash
make schema.sql
```

## Testing

Run go test with the appropriate
[libpq-style environment variables](https://www.postgresql.org/docs/current/libpq-envars.html)
set. If the database connection fails, database integration tests will
be skipped. If PGDATABASE is unset, relui-test is created, migrated, 
and used by default.

```bash
PGHOST=localhost PGUSER=postgres go test -v ./... ../../internal/relui/...
```

Alternatively, using docker:
```bash
make test
```
