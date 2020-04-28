# issueswebhook Cloud Function

```sh
gcloud functions deploy GitHubIssueChangeWebHook \
  --project=symbolic-datum-552 \
  --runtime go113 \
  --trigger-http \
  --set-env-vars=GCS_BUCKET=<bucket name>,GITHUB_WEBHOOK_SECRET=<github webhook secret>
```
