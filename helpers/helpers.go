package helpers

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

// FetchRawFromURL fetches raw HTML encoding recipe content from the given URL.
func FetchRawFromURL(url string) (io.ReadCloser, error) {
	httpClient := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; RecipeFetcher/1.0)")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}

	if !(resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadGateway) {
		return nil, fmt.Errorf("failed to fetch URL: received status code %d", resp.StatusCode)
	}

	return resp.Body, nil
}

// CreateMarkdownFromRaw converts HTML-encoded recipe content and returns it in
// markdown format. Helpful when passing web content to an LLM.
func CreateMarkdownFromRaw(domainURL, content string) (string, error) {
	u, err := url.Parse(domainURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse domain URL: %w", err)
	}
	converter := md.NewConverter(u.Scheme+"://"+u.Hostname(), true, nil)
	markdown, err := converter.ConvertString(content)
	if err != nil {
		return "", fmt.Errorf("failed to convert HTML to markdown: %w", err)
	}

	return markdown, nil
}
