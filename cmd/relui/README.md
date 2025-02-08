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
PGHOST=localhost PGDATABASE=relui-dev PGUSER=postgres go run . -listen-http=localhost:8080
```

Alternatively, using docker:

```bash
make dev
```

### Enable Cloud Build in your local relui

Set your personal Google Cloud Project.

```bash
gcloud config set project PROJECT_ID
```

Enable the Cloud Build API from your project.

```bash
gcloud services enable cloudbuild.googleapis.com
```

Create a GCS bucket in your project.
```bash
gcloud storage buckets create gs://GCS_BUCKET_NAME
```

Run relui with additional parameters `-cloud-build-project` and
`-scratch-files-base`. The local relui will run cloud build in you project and
copy the results to your GCS bucket.
```bash
PGHOST=localhost PGDATABASE=relui-dev PGUSER=postgres go run . -listen-http=localhost:8080 -cloud-build-project PROJECT_ID -scratch-files-base gs://GCS_BUCKET_NAME
```

### Enable GitHub Client in your local relui

Use github [token](https://github.com/settings/tokens/new) page to generate a
new access token.

Run relui with additional parameters `-github-token`. The local relui will use
your personal credential to talk to github.

```bash
PGHOST=localhost PGDATABASE=relui-dev PGUSER=postgres go run . -listen-http=localhost:8080 -github-token TOKEN
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

Migrations are automatically ran on application launch. "Down"
migrations are not automatically run and must be manually invoked in
`psql`, or by the `--migrate-down-up` flag or `make migrate-down-up`.

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

## Deployment

See the documentation on [deployment](../../doc/deployment.md).

## JS/CSS formatting and lint

This project uses eslint and stylelint to format JavaScript and CSS files.

To run:

```bash
npm run lint
```

Alternatively, using Docker:

```bash
make lint
```
