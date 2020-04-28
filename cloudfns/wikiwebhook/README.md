# wikiwebhook Cloud Function

```sh
gcloud functions deploy GitHubWikiChangeWebHook \
  --project=symbolic-datum-552 \
  --runtime go113 \
  --trigger-http \
  --set-env-vars=PUBSUB_TOPIC=github.webhooks.golang.go.wiki,GITHUB_WEBHOOK_SECRET=<github webhook secret>
```
