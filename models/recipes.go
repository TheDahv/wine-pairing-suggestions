package models

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

// Suggestion models a wine pairing recommendation from an LLM prompt response
// encoded in JSON format.
type Suggestion struct {
	Style        string `json:"style"`
	Region       string `json:"region"`
	VintageRange string `json:"vintageRange"`
	Description  string `json:"description"`
	PairingNote  string `json:"pairingNote"`
}

// SummarizeRecipe takes a markdown representation of a recipe published on the
// Internet and returns a summary that would be helpful to someone making wine
// pairing recommendations for that recipe.
func SummarizeRecipe(ctx context.Context, model llms.Model, markdown string) (string, error) {
	prompt := fmt.Sprintf(`
	Role: You a wine-minded foodie who can summarize recipes in a way that
	would be helpful for a sommelier to pair a wine with the dish.

	Context: You are reviewing the following recipe to look for a high-level
	summary of the dish:
	
	<RECIPE>
	%s
	</RECIPE>
	
	Task: Generate a one-paragraph summary of the meal that the recipe
	describes, highlighting the most important tasting notes and flavors that
	would guide a wine pairing. Include a list of the key ingredients, ordered
	by their importance and influence on the overall flavor profile of the dish.
	Output the response directly without introducing it.
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

// GeneratePairingSuggestions takes a summary of a recipe and generates wine pairing suggestions.
// The prompt directs the model to return suggestions in JSON format conforming to the type specified
// by Suggestion.
func GeneratePairingSuggestions(ctx context.Context, model llms.Model, summary string) (string, error) {
	prompt := fmt.Sprintf(`
		Role: You are a wine-minded foodie who wants to make wine accessible to
		everyone, particularly focusing on wine's relationship with food. Rather
		than being highbrow and inaccessible, you bias for approachable
		suggestions that are easy to understand.

		Context: You are given a recipe in markdown format with an intent to
		think about wines that would pair well:

		<RECIPE_SUMMARY>
		%s
		</RECIPE_SUMMARY>

		Output format: a JSON array of objects with the following fields:
		- style:string
		- region:string
		- vintageRange:string
		- description:string
		- pairingNote:string

		The output MUST match this JSON format every time.

		Task: Generate up to ten wine pairings in JSON format, naming the style (e.g.,
		Vinho Verde, Zinfandel, Pinot Noir), region (e.g., Bordeaux, Lodi Valley, Northern Spain),
		vintage range (e.g., 2013-2020), a one-sentence description of the wine, and a
		one-sentence summary on why the suggestion pairs well with the dish.
		`,
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
