// Package models contains functions and operations concerned with creating LLM
// models from various providers and issuing prompts to them. "Make*Model"
// functions in this package return `langchaingo.llms.Model` instances
// configured to talk to the upstream provider.
package models

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/bedrock"
)

// MakeBedrockModel establishes a connection to AWS Bedrock. When working
// locally, the code assumes the local environment has an AWS credentials for a
// properly configured IAM role loaded in environment variables (see
// `env.example`). It's configured to return a Claude 3.5 Haiku model. It
// returns a LangChain instance configured for Bedrock.
func MakeBedrockModel(ctx context.Context) (llms.Model, error) {
	// cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(awsProfileName))
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("unable to load SDK config, %v\n", err)
	}

	client := bedrockruntime.NewFromConfig(cfg)
	llm, err := bedrock.New(
		bedrock.WithClient(client),
		// Note, this version of the Bedrock SDK doesn't have this supported model name yet
		bedrock.WithModel("anthropic.claude-3-5-haiku-20241022-v1:0"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Bedrock LLM: %w", err)
	}

	return llm, nil
}
