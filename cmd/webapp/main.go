package main

import (
	"log"

	"github.com/thedahv/wine-pairing-suggestions/webapp"
)

func main() {
	wa, err := webapp.NewWebapp(3000)
	if err != nil {
		log.Fatalf("unable to build webapp: %v", err)
	}

	if err := wa.Start(3000); err != nil {
		log.Fatalf("unable to start server: %v", err)
	}
}
