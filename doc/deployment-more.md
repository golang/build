# More deployment notes

Services in x/build that are deployed to our GKE cluster all follow the same
workflow. See deployment.md for the common path.

This file holds more notes for less common paths.

## Building services with local Docker

### First-time setup

Install the `docker` command.

Verify that `docker run hello-world` works without `sudo`. (You may need to run
`sudo adduser $USER docker`, then either log out and back in or run `newgrp
docker`.)

Then run:

```sh
$ gcloud auth configure-docker
```
