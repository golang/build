# perf.golang.org

Deploy:

1. `gcloud app deploy --project=golang-org --no-promote app.yaml`

2. Find the new version in the
[Cloud Console](https://console.cloud.google.com/appengine/versions?project=golang-org&serviceId=perf).

3. Check that the deployed version is working (click the website link in the version list).

4. If all is well, click "Migrate Traffic" to move 100% of the perf.golang.org traffic to the new version.

5. You're done.
