// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relworker is a worker process for managing the release process of Go.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"cloud.google.com/go/pubsub"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	projectID = flag.String("project-id", os.Getenv("PUBSUB_PROJECT_ID"), "Pubsub project ID for communicating with relui. Uses PUBSUB_PROJECT_ID by default.")
	topicID   = flag.String("topic-id", "relui-development", "Pubsub topic ID for communicating with relui.")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	sub := getSubscription(ctx, *projectID, *topicID, "relworker")
	log.Fatal(sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		log.Println(string(msg.Data))
		msg.Ack()
	}))
}

// getSubscription creates and returns a pubsub subscription from the project
// specified in projectId, which is to be used for communicating with relui.
//
// It is safe to call if a subscription already exists. A reference to the
// subscription will be returned.
func getSubscription(ctx context.Context, projectID, topicID, subscriptionID string) *pubsub.Subscription {
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("pubsub.NewClient(_, %q) = %v, wanted no error", projectID, err)
	}
	t := client.Topic(topicID)
	// TODO(golang.org/issue/40279): determine if these values are appropriate, move to const/config.
	scfg := pubsub.SubscriptionConfig{Topic: t, AckDeadline: 10 * time.Second, ExpirationPolicy: 24 * time.Hour}
	_, err = client.CreateSubscription(ctx, subscriptionID, scfg)
	if err != nil && status.Code(err) != codes.AlreadyExists {
		log.Fatalf("client.CreateSubscription(_, %q, %v) = _, %q, wanted no error or already exists error", subscriptionID, scfg, err)
	}
	return client.Subscription(subscriptionID)
}
