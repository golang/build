package kubernetes_test

import (
	"log"
	"net/http"

	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/api"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
)

func ExampleRunPod() {
	kube, err := kubernetes.NewClient("https://example.com", &http.Client{
		Transport: &oauth2.Transport{
			Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "aCcessWbU3toKen"}),
		},
	})
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}
	podResult, err := kube.RunLongLivedPod(context.Background(), &api.Pod{
		TypeMeta: api.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: api.ObjectMeta{
			Name: "redis-pod-example",
			Labels: map[string]string{
				"tag": "prod",
			},
		},
		Spec: api.PodSpec{
			Containers: []api.Container{
				{
					Name:  "redis-container",
					Image: "redis:alpine",
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("failed to run pod: %v", err)
	}
	log.Printf("pod created: %#v", podResult)
	logs, err := kube.PodLog(context.Background(), "redis-pod-example")
	if err != nil {
		log.Fatalf("failed to get pod logs: %v", err)
	}
	log.Printf("pod logs: %q", logs)
}
