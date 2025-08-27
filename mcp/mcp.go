package mcp

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/thedahv/wine-pairing-suggestions/cache"
	"github.com/thedahv/wine-pairing-suggestions/helpers"
)

func MakeServer(c cache.Cacher) *server.MCPServer {
	s := server.NewMCPServer(
		"Wine Suggestions Helper Tools",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, false),
		server.WithPromptCapabilities(false),
		server.WithRecovery(),
		server.WithLogging(),
	)

	AddSiteFetchTool(s, c)
	AddCacheGetTool(s, c)
	AddCacheWriteTool(s, c)

	return s
}

func AddSiteFetchTool(server *server.MCPServer, cache cache.Cacher) {
	server.AddTool(mcp.NewTool(
		"FetchSite",
		mcp.WithDescription("Given a URL, fetch a website and return its contents in Markdown format"),
		mcp.WithString(
			"URL",
			mcp.Required(),
			mcp.Description("The URL for the site to fetch"),
		),
		mcp.WithOutputSchema[string](),
		mcp.WithIdempotentHintAnnotation(true),
	),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			l := log.New(log.Default().Writer(), "[Tool=FetchSite] ", log.Default().Flags())
			u := request.GetString("URL", "")
			if u == "" {
				l.Printf("Tool called without a URL")
				return mcp.NewToolResultError("a URL is required"), nil
			}

			l.Printf("Fetching contents for %s\n", u)
			contents, err := cache.GetOrFetch(fmt.Sprintf("recipes:raw:%s", u), func() (string, error) {
				l.Println("Raw cache miss:", u)
				resp, err := helpers.FetchRawFromURL(u)
				if err != nil {
					return "", fmt.Errorf("unable to fetch URL: %v", err)
				}
				defer resp.Close()

				contents, err := io.ReadAll(resp)
				if err != nil {
					return "", fmt.Errorf("unable to read response: %v", err)
				}

				return string(contents), nil
			})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("unable to fetch site", err), nil
			}

			l.Printf("Converting %s to markdown\n", u)
			parsed, err := cache.GetOrFetch(fmt.Sprintf("recipes:parsed:%s", u), func() (string, error) {
				l.Printf("Markdown cache miss: %s", u)
				return helpers.CreateMarkdownFromRaw(u, contents)
			})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("unable to generate markdown from site contents", err), nil
			}

			l.Println("Returning parsed contents for", u)
			return mcp.NewToolResultText(parsed), nil
		},
	)
}

type CacheResult struct {
	Ok    bool   `json:"ok"`
	Value string `json:"value"`
}

func AddCacheGetTool(server *server.MCPServer, c cache.Cacher) {
	server.AddTool(
		mcp.NewTool(
			"CacheGet",
			mcp.WithDescription("Given a key, fetch a value from the application cache"),
			mcp.WithString("key", mcp.Description("The key for the cache item to fetch"), mcp.Required()),
			mcp.WithOutputSchema[CacheResult](),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			l := log.New(log.Default().Writer(), "[Tool=CacheGet] ", log.Default().Flags())
			var result CacheResult

			key := request.GetString("key", "")
			if key == "" {
				l.Println("Called without a key")
				return mcp.NewToolResultError("key is required"), nil
			}

			l.Println("Fetching cache for:", key)
			value, err := c.Get(key)

			if err == cache.ErrKeyNotFound {
				l.Println("Cache miss: ", key)
				return mcp.NewToolResultStructured(result, ""), nil
			}
			if err != nil {
				l.Println("Cache fetch error:", err)
				return mcp.NewToolResultErrorFromErr("error fetching from cache", err), nil
			}

			l.Println("Successfully fetched from cache at key", key)
			result.Value = value
			result.Ok = true
			return mcp.NewToolResultStructured(result, value), nil
		},
	)
}

func AddCacheWriteTool(server *server.MCPServer, cache cache.Cacher) {
	server.AddTool(
		mcp.NewTool(
			"CacheWrite",
			mcp.WithDescription("Given a key and a value, write that value to cache"),
			mcp.WithString("key", mcp.Description("The key for the cache item to write"), mcp.Required()),
			mcp.WithString("value", mcp.Description("The value to store"), mcp.Required()),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			l := log.New(log.Default().Writer(), "[Tool=CacheWrite] ", log.Default().Flags())
			key := request.GetString("key", "")
			value := request.GetString("value", "")
			l.Println("Writing cache for: ", key)
			if key == "" {
				l.Println("Called without key")
				return mcp.NewToolResultError("key is required"), nil
			}
			err := cache.Set(key, value)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("unable to write cache", err), nil
			}

			l.Println("successfully wrote to cache at key", key)
			return mcp.NewToolResultText("successfully written"), nil
		},
	)
}

func AddContentsHasherTool(server *server.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"HashRecipeSummary",
			mcp.WithDescription("Generate a consistent hash for recipe text content to use as a cache key. Creates a deterministic identifier for recipe summaries and wine pairing suggestions."),
			mcp.WithString("content", mcp.Description("The recipe text content to hash. Can be raw recipe description, ingredients list, or cooking instructions."), mcp.Required()),
			mcp.WithOutputSchema[string](),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			l := log.New(log.Default().Writer(), "[Tool=HashRecipeSummary] ", log.Default().Flags())
			content := request.GetString("content", "")
			if content == "" {
				l.Println("called without content argument")
				return mcp.NewToolResultError("content is required"), nil
			}

			return mcp.NewToolResultText(helpers.HashContent(content)), nil
		},
	)
}
