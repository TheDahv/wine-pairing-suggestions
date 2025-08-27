// Package models contains functions and operations concerned with creating LLM
// models from various providers and issuing prompts to them. "Make*Model"
// functions in this package return `langchaingo.llms.Model` instances
// configured to talk to the upstream provider.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/bedrock"
	"github.com/tmc/langchaingo/tools"
)

const awsClaudeKeySecret = "prod/Anthropic/WineSuggestions"

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

const claudeModelId = "claude-3-5-haiku-latest"

// MakeClaude connects to claude assuming the ANTHROPIC_API_KEY environment variable
// is set with a valid token.
func MakeClaude(ctx context.Context) (llms.Model, error) {
	var anthropicKey string
	if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
		anthropicKey = k
	} else {
		k, err := getAWSSecret(awsClaudeKeySecret)
		if err != nil {
			return nil, fmt.Errorf("unable to get an Anthropic key: %v", err)
		}

		anthropicKey = k
	}

	llm, err := anthropic.New(anthropic.WithModel(claudeModelId), anthropic.WithToken(anthropicKey))
	if err != nil {
		return llm, fmt.Errorf("unable to connect to Anthropic: %v", err)
	}

	return llm, nil
}

// Summary models a recipe summary, taking into account that the LLM may choose
// not to summarize if it thinks there's an issue with the input.
type Summary struct {
	Ok          bool   `json:"ok"`
	Summary     string `json:"summary"`
	AbortReason string `json:"abortReason"`
}

// Suggestion models a wine pairing recommendation from an LLM prompt response
// encoded in JSON format.
type Suggestion struct {
	Style       string `json:"style"`
	Region      string `json:"region"`
	Description string `json:"description"`
	PairingNote string `json:"pairingNote"`
}

// SummarizeRecipe takes a markdown representation of a recipe published on the
// Internet and returns a summary that would be helpful to someone making wine
// pairing recommendations for that recipe.
func SummarizeRecipe(ctx context.Context, model llms.Model, markdown string) (string, error) {
	prompt := fmt.Sprintf(`
	Summarize this recipe for wine pairing. Focus on flavors and key ingredients.

	<RECIPE>
	%s
	</RECIPE>

	Create a one-paragraph summary highlighting:
	- Primary flavors (sweet, salty, acidic, bitter, umami)
	- Cooking methods (grilled, braised, roasted, etc.)
	- Key ingredients by flavor impact (most important first)
	- Sauce/seasoning profile
	- Overall dish weight (light, medium, heavy)

	Respond in this exact JSON format:
	{
		"ok": boolean,
		"abortReason": string,
		"summary": string
	}

	Success: {"ok": true, "abortReason": "", "summary": "This hearty beef stew features..."}
	Failure: {"ok": false, "abortReason": "Not a recipe", "summary": ""}

	Abort if content is:
	- Not food/recipe related
	- Unsafe/malicious
	- Too unclear to summarize
	`, markdown)

	summary, err := llms.GenerateFromSinglePrompt(
		ctx,
		model,
		prompt,
	)

	if err != nil {
		return "", fmt.Errorf("failed to generate recipe summary: %w", err)

	}

	return summary, nil
}

// ParseSummary parses LLM output into Go types using JSON type annotations
// from Summary.
func ParseSummary(output string) (Summary, error) {
	var s Summary
	if err := json.Unmarshal([]byte(output), &s); err != nil {
		return s, fmt.Errorf("unable to parse Summary output: %v", err)
	}

	return s, nil
}

// GeneratePairingSuggestions takes a summary of a recipe and generates wine pairing suggestions.
// The prompt directs the model to return suggestions in JSON format conforming to the type specified
// by Suggestion.
func GeneratePairingSuggestions(ctx context.Context, model llms.Model, summary string) (string, error) {
	prompt := fmt.Sprintf(`
	Suggest approachable wine pairings for this dish. Focus on accessible wines people can actually find.

	<RECIPE_SUMMARY>
	%s
	</RECIPE_SUMMARY>

	Generate 5-10 wine pairings as JSON array. For each wine:
	- Match the dish's weight and primary flavors
	- Choose wines available at most wine shops
	- Explain pairing logic simply

	JSON format (exact structure required):
	[
		{
			"style": "wine style name",
			"region": "specific region",
			"description": "one sentence about the wine",
			"pairingNote": "one sentence why it pairs well"
		}
	]

	Example:
	[
		{
			"style": "Cabernet Sauvignon",
			"region": "Washington State",
			"description": "Full-bodied red with dark fruit and moderate tannins.",
			"pairingNote": "The wine's structure complements the rich beef while fruit balances the umami."
		}
	]`,
		summary,
	)

	answer, err := llms.GenerateFromSinglePrompt(ctx, model, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to generate wine suggestions: %v", err)
	}

	return answer, nil
}

func GeneratePairingSuggestionsV2(ctx context.Context, model llms.Model, tools []tools.Tool, input string) (string, error) {
	prompt := fmt.Sprintf(`
	Generate wine pairings for the user's recipe input. Use tools to fetch and cache web content.

	<USER_INPUT>
	%s
	</USER_INPUT>

	Process:
	1. Check if input is URL or recipe text
	2. Validate input is actually about food/recipes - abort with error if not
	3. For URLs: 
	- Use FetchSite to get content (this handles Raw->Parsed caching automatically)
	- Check CacheGet("recipes:summarized:<URL>") for existing wine-pairing summary
	- If no cached summary: Create recipe summary focusing on wine pairing elements from the parsed content, then CacheWrite("recipes:summarized:<URL>", summary)
	4. For recipe text:
	- Use HashRecipeSummary to get content hash
	- Check CacheGet("recipes:summarized:<hash>") for existing wine-pairing summary  
	- If no cached summary: Create recipe summary focusing on wine pairing elements, then CacheWrite("recipes:summarized:<hash>", summary)
	7. If no cached pairings: Generate 5-10 new wine pairings.

	NOTE: FetchSite tool automatically handles caching raw HTML at
	"recipes:raw:<URL>" and parsed Markdown at "recipes:parsed:<URL>". The model
	only needs to handle the third phase: creating and caching wine-pairing
	summaries at "recipes:summarized:<URL>".

	Recipe summary should include:
	- Primary flavors and cooking methods
	- Key ingredients (most important first)  
	- Dish weight (light/medium/heavy)
	- Sauce/seasoning profile

	Wine pairing criteria:
	- Match dish weight and flavors
	- Accessible wines from common shops
	- Simple pairing explanations

	Don't explain your answer after you create the JSON. Let the JSON be the final answer.

	Always return this JSON format:
	{
		"suggestions": [
			{
			"style": "wine name",
			"region": "wine region", 
			"description": "one sentence wine description",
			"pairingNote": "one sentence pairing reason"
			}
		],
		"summary": "one paragraph summary of the recipe highlighting flavors, cooking methods, key ingredients, and dish weight",
		"error": string or null
	}

	Error handling:
	- Non-recipe content (URLs or text): Return error "Content is not about food or recipes"
	- Failed fetches: Return error
	- Invalid input: Return error	 
	`, input)

	agent := agents.NewOneShotAgent(model, tools, agents.WithMaxIterations(3))
	//executor := agents.NewExecutor(agent, agents.WithCallbacksHandler(callbacks.LogHandler{}))
	executor := agents.NewExecutor(agent)

	result, err := chains.Run(ctx, executor, prompt)
	if err != nil {
		return "", fmt.Errorf("agent run error: %v", err)
	}

	var sb strings.Builder
	for l := range strings.Lines(result) {
		if strings.HasPrefix(result, "Thought: ") || strings.HasPrefix(result, "Action :") || strings.HasPrefix(result, "Action Input:") {
			continue
		}

		sb.WriteString(l)
	}

	return strings.TrimSpace(sb.String()), nil
}

type SuggestionsResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
	Summary     string       `json:"summary"`
	ErrorMsg    string       `json:"error,omitempty"`
}

func ParseSuggestionsV2(output string) (SuggestionsResponse, error) {
	var r SuggestionsResponse
	err := json.Unmarshal([]byte(output), &r)
	if err != nil {
		return r, fmt.Errorf("could not parse response: %v", err)
	}

	if r.ErrorMsg != "" {
		return r, fmt.Errorf("agent detected an error: %v", r.ErrorMsg)
	}

	return r, nil
}

// ParseSuggestions parses LLM output into a Go type using JSON type annotations
// from Suggestion.
func ParseSuggestions(output string) ([]Suggestion, error) {
	var parsed []Suggestion
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return parsed, fmt.Errorf("suggestion parse error: %v", err)
	}

	return parsed, nil
}

type anthropicResponse struct {
	Key string `json:"ANTHROPIC_WINESUGGESTIONS"`
}

func getAWSSecret(secret string) (string, error) {
	ctx := context.Background()

	config, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("unable to create AWS context: %v", err)
	}

	// Create Secrets Manager client
	svc := secretsmanager.NewFromConfig(config)

	input := &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(secret),
		VersionStage: aws.String("AWSCURRENT"), // VersionStage defaults to AWSCURRENT if unspecified
	}

	result, err := svc.GetSecretValue(ctx, input)
	if err != nil {
		// For a list of exceptions thrown, see
		// https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetSecretValue.html
		return "", fmt.Errorf("unable to get secret: %v", err)
	}

	// Decrypts secret using the associated KMS key.
	var secretString string = *result.SecretString
	var r anthropicResponse
	if err := json.Unmarshal([]byte(secretString), &r); err != nil {
		return "", fmt.Errorf("unable to parse secret response: %v", err)
	}

	return r.Key, nil
}
