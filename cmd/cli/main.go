package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/thedahv/wine-pairing-suggestions/helpers"
	"github.com/thedahv/wine-pairing-suggestions/models"
	"github.com/tmc/langchaingo/llms"
)

func main() {
	args := os.Args[1:]
	if len(args) != 1 {
		log.Fatalf("Usage: %s <recipe-url>", os.Args[0])
	}

	recipeURL := args[0]

	ctx := context.Background()
	model, err := models.MakeBedrockModel(ctx)

	if err != nil {
		log.Fatal(err)
	}

	spinner := spinner.New(spinner.CharSets[9], 100*time.Millisecond)

	rdr, err := helpers.FetchRawFromURL(recipeURL)
	fmt.Println("Fetching the recipe.")
	spinner.Start()
	if err != nil {
		log.Fatal("unable to fetch recipe:", err)
	}
	spinner.Stop()

	raw, err := io.ReadAll(rdr)
	if err != nil {
		log.Fatal("unable to read raw response:", err)
	}

	fmt.Println("Summarizing the recipe.")
	spinner.Start()
	markdown, err := helpers.CreateMarkdownFromRaw(recipeURL, string(raw))
	if err != nil {
		log.Fatal("unable to create markdown from raw:", err)
	}

	summary, err := models.SummarizeRecipe(ctx, model, markdown)
	if err != nil {
		log.Fatal("unable to summarize recipe:", err)
	}
	spinner.Stop()

	fmt.Println("Recipe Summary:")
	fmt.Println(summary)
	fmt.Println()
	fmt.Println()

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

		Task: Generate up to ten wine pairings, describing the wine name,
		producer, and vintage. Offer a one-sentence tasting notes for the wine
		and then another sentence on why it pairs well with the dish.`,
		summary,
	)

	fmt.Println("Generating wine pairings.")
	spinner.Start()
	answer, err := llms.GenerateFromSinglePrompt(ctx, model, prompt)
	if err != nil {
		log.Fatal(err)
	}
	spinner.Stop()

	fmt.Println()
	fmt.Println()
	fmt.Println(answer)
}
