package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/thedahv/wine-pairing-suggestions/models"
	"github.com/thedahv/wine-pairing-suggestions/webapp"
)

func main() {
	host := os.Getenv("REDIS_HOST")
	cachePort := func() int {
		port, err := strconv.ParseInt(os.Getenv("REDIS_PORT"), 10, 64)
		if err != nil {
			return 6379
		}
		return int(port)
	}()

	var serverPort int
	if p, err := strconv.ParseInt(os.Getenv("PORT"), 10, 64); err != nil {
		log.Fatalf("unable to parse PORT environment variable: %v", err)
	} else {
		serverPort = int(p)
	}

	ctx := context.Background()

	model, err := models.MakeBedrockModel(ctx)
	if err != nil {
		log.Fatalf("unable to create model: %v", err)
	}

	wa, err := webapp.NewWebapp(serverPort,
		webapp.WithRedisCache(host, cachePort),
		webapp.WithGoogleClientID(os.Getenv("GOOGLE_CLIENT_ID")),
		webapp.WithHostname(os.Getenv("HOSTNAME")),
		webapp.WithModel(model),
	)

	if err != nil {
		log.Fatalf("unable to build webapp: %v", err)
	}

	if err := wa.Start(); err != nil {
		log.Fatalf("unable to start server: %v", err)
	}
}
