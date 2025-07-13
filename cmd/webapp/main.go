package main

import (
	"log"
	"os"
	"strconv"

	"github.com/thedahv/wine-pairing-suggestions/webapp"
)

func main() {
	host := os.Getenv("REDIS_HOST")
	port := func() int {
		port, err := strconv.ParseInt(os.Getenv("REDIS_PORT"), 10, 64)
		if err != nil {
			return 6379
		}
		return int(port)
	}()

	wa, err := webapp.NewWebapp(3000, webapp.WithRedisCache(host, port))

	if err != nil {
		log.Fatalf("unable to build webapp: %v", err)
	}

	if err := wa.Start(3000); err != nil {
		log.Fatalf("unable to start server: %v", err)
	}
}
