package main

import (
	"context"
	"fmt"
	"log"

	"github.com/erikturner/challenge/internal/kube"
)

func main() {
	ctx := context.Background()

	client, err := kube.New(ctx, "default")
	if err != nil {
		log.Fatal(err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		log.Fatal(err)
	}

	deployments, err := client.ListDeployments(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Deployments:")
	for _, deployment := range deployments {
		fmt.Printf("%s\n", deployment.Name)
	}

	// Test GetReplicas
	replicasGet, err := client.GetReplicas(ctx, "demo")
	if err != nil {
		log.Fatal(err)
	}

	// Test SetReplicas
	replicasSet, err := client.SetReplicas(ctx, "demo", 5)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("replicas before update: %d\n", replicasGet)
	fmt.Printf("updated replicas: %d\n", replicasSet)

	// Test informer/watch
	fmt.Println()
	fmt.Println("Run this in another terminal:")
	fmt.Println("kubectl scale deployment demo --replicas=7")
	fmt.Println()
	fmt.Println("Press Enter after changing replicas with kubectl...")
	fmt.Scanln()

	replicasCache, err := client.GetReplicas(ctx, "demo")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("demo replicas from informer cache: %d\n", replicasCache)
}
