package models

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

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
	fmt.Println("parseing summary output")
	fmt.Println(output)
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

// ParseSuggestions parses LLM output into a Go type using JSON type annotations
// from Suggestion.
func ParseSuggestions(output string) ([]Suggestion, error) {
	var parsed []Suggestion
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return parsed, fmt.Errorf("suggestion parse error: %v", err)
	}

	return parsed, nil
}
