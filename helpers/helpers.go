package helpers

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/golang-jwt/jwt/v5"
)

const googleCertsURL = "https://www.googleapis.com/oauth2/v3/certs"

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

type googleKeysResponse struct {
	Keys []struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		TokenN    string `json:"n"`
		TokenE    string `json:"e"`
	} `json:"keys"`
}

// Claims models the Google credential response payload. See:
// https://developers.google.com/identity/gsi/web/reference/js-reference#CredentialResponse
type Claims struct {
	AccountID string `json:"sub"`
	Email     string `json:"email"`
	jwt.RegisteredClaims
}

func GetGoogleJWTToken(algorithm string) (*rsa.PublicKey, error) {
	key := &rsa.PublicKey{}

	c := http.Client{}
	resp, err := c.Get(googleCertsURL)
	if err != nil {
		return key, fmt.Errorf("unable to fetch from Google: %v", err)
	}
	defer resp.Body.Close()

	contents, err := io.ReadAll(resp.Body)
	if err != nil {
		return key, fmt.Errorf("unable to read response body: %v", err)
	}

	var response googleKeysResponse
	if err := json.Unmarshal(contents, &response); err != nil {
		return key, fmt.Errorf("unable to parse response JSON: %v", err)
	}

	for _, k := range response.Keys {
		if k.Algorithm == algorithm {
			if !(k.TokenE == "AQAB" || k.TokenE == "AAEAAQ") {
				return key, fmt.Errorf("unrecognized exponent: %s", k.TokenE)
			}
			key.E = 65537

			nb, err := base64.RawURLEncoding.DecodeString(k.TokenN)
			if err != nil {
				return key, fmt.Errorf("unable to decode N: %v", err)
			}
			key.N = new(big.Int).SetBytes(nb)

			return key, nil
		}
	}

	return key, fmt.Errorf("algorithm '%s' was not in certificates response", algorithm)
}
