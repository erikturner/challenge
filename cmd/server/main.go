package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/erikturner/challenge/internal/api"
	"github.com/erikturner/challenge/internal/kube"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx := context.Background()

	client, err := kube.New(ctx, "default")
	if err != nil {
		log.Fatal(err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		log.Fatal(err)
	}

	handler := api.New(client, logger)

	fmt.Println("API listening on http://localhost:8080")

	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatal(err)
	}
}
