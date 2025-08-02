package main

import (
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	lambdaHandler "github.com/thedahv/wine-pairing-suggestions/lambda"
)

func main() {
	handler, err := lambdaHandler.NewHandler()
	if err != nil {
		log.Fatalf("Failed to initialize Lambda handler: %v", err)
	}

	lambda.Start(handler.HandleRequest)
}
