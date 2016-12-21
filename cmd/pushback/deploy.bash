#!/bin/bash

go get google.golang.org/appengine/cmd/aedeploy
aedeploy gcloud --project symbolic-datum-552 app deploy --no-promote app.yaml
