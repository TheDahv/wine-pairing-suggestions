package lambda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	helpers "github.com/thedahv/wine-pairing-suggestions/lambdahelpers"
	"github.com/thedahv/wine-pairing-suggestions/models"
	"github.com/thedahv/wine-pairing-suggestions/webapp"
)

// Handler represents the Lambda handler with shared resources
type Handler struct {
	webapp *webapp.Webapp
}

// NewHandler creates a new Lambda handler with initialized dependencies
func NewHandler() (*Handler, error) {
	ctx := context.Background()

	// Initialize model
	model, err := models.MakeClaude(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create model: %v", err)
	}

	// Prepare webapp options
	var options []webapp.Option

	// Add cache option
	if cacheEndpoint := os.Getenv("VALKEY_ENDPOINT"); cacheEndpoint != "" {
		parts := strings.Split(cacheEndpoint, ":")
		host := parts[0]
		port := 6379
		if len(parts) > 1 {
			p, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil {
				port = int(p)
			}
		}
		log.Printf("with cache: h=%s, p=%d\n", host, port)
		options = append(options, webapp.WithRedisCache(host, port))
	} else {
		log.Println("using memory cache")
		options = append(options, webapp.WithMemoryCache())
	}

	// Add other options
	if clientID := os.Getenv("GOOGLE_CLIENT_ID"); clientID != "" {
		options = append(options, webapp.WithGoogleClientID(clientID))
	}
	if hostname := os.Getenv("HOSTNAME"); hostname != "" {
		options = append(options, webapp.WithHostname(hostname))
	}
	options = append(options, webapp.WithModel(model))

	// Create webapp with serverless-friendly options
	wa, err := webapp.NewWebapp(0, options...) // Port not used in Lambda
	if err != nil {
		return nil, fmt.Errorf("unable to create webapp: %v", err)
	}

	return &Handler{
		webapp: wa,
	}, nil
}

// HandleRequest processes API Gateway requests
func (h *Handler) HandleRequest(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	// Convert API Gateway request to http.Request
	httpReq, err := h.convertToHTTPRequest(request)
	if err != nil {
		return h.errorResponse(500, fmt.Sprintf("request conversion error: %v", err)), nil
	}

	// Create response recorder
	recorder := newResponseRecorder()

	// Route the request
	h.routeRequest(recorder, httpReq)

	// Convert back to API Gateway response
	return h.convertToAPIGatewayResponse(recorder), nil
}

// convertToHTTPRequest converts API Gateway request to standard http.Request
func (h *Handler) convertToHTTPRequest(request events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	// Build URL
	scheme := "https"
	if request.Headers["X-Forwarded-Proto"] != "" {
		scheme = request.Headers["X-Forwarded-Proto"]
	}

	host := request.Headers["Host"]
	if host == "" {
		host = "localhost"
	}

	path := request.RawPath
	if len(request.QueryStringParameters) > 0 {
		params := url.Values{}
		for key, value := range request.QueryStringParameters {
			params.Add(key, value)
		}
		path += "?" + params.Encode()
	}

	url, err := url.Parse(fmt.Sprintf("%s://%s%s", scheme, host, path))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}

	// Create request body
	var body io.Reader
	if request.Body != "" {
		if request.IsBase64Encoded {
			// Handle base64 encoded body (binary data)
			// For now, assume text data
			db, err := base64.StdEncoding.DecodeString(request.Body)
			if err != nil {
				return nil, fmt.Errorf("unable to parse body: %v", err)
			}
			body = strings.NewReader(string(db))
		} else {
			body = strings.NewReader(request.Body)
		}
	}

	// Create HTTP request
	req, err := http.NewRequest(request.RequestContext.HTTP.Method, url.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Set headers
	for name, value := range request.Headers {
		req.Header.Set(name, value)
	}

	// Set request cookies
	for _, cookieString := range request.Cookies {
		// Parse the cookie string into a http.Cookie object
		// The net/http package doesn't directly provide a way to parse a single cookie string,
		// but you can simulate it by creating a dummy header and using http.Request's Cookies() method.
		h := http.Header{}
		h.Add("Cookie", cookieString)
		dr := &http.Request{Header: h}
		parsed := dr.Cookies() // Parse the cookies

		for _, c := range parsed {
			req.AddCookie(c) // Add the parsed cookie to the new request
		}
	}

	// TODO build incoming form if present

	// Add path parameters to context if needed
	if len(request.PathParameters) > 0 {
		ctx := req.Context()
		for key, value := range request.PathParameters {
			ctx = context.WithValue(ctx, key, value)
		}
		req = req.WithContext(ctx)
	}

	return req, nil
}

// routeRequest handles routing logic similar to the original webapp
func (h *Handler) routeRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	switch {
	case method == "POST" && strings.HasPrefix(path, "/recipes/summary/"):
		// TODO handle error
		u := strings.TrimPrefix(path, "/recipes/summary/")
		decoded, _ := url.QueryUnescape(u)
		log.Printf("Preparing summary for URL (path=%s, unescaped=%s, escaped=%s)\n ", path, u, decoded)
		r = h.setPathValue(r, "url", decoded)
		h.webapp.WithSessionRequired(h.webapp.WithSufficientQuota(h.webapp.PostCreateRecipe))(w, r)
	case method == "GET" && path == "/recipes/suggestions/recent":
		h.webapp.WithSessionRequired(h.webapp.GetRecentSuggestions)(w, r)
	case method == "GET" && strings.HasPrefix(path, "/recipes/suggestions/"):
		// TODO handle error
		u := strings.TrimPrefix(path, "/recipes/suggestions/")
		decoded, _ := url.QueryUnescape(u)
		log.Printf("Preparing suggestions for URL (path=%s, unescaped=%s, escaped=%s)\n ", path, u, decoded)
		r = h.setPathValue(r, "url", decoded)
		h.webapp.WithSessionRequired(h.webapp.WithSufficientQuota(h.webapp.GetRecipeWineSuggestions))(w, r)
	case method == "GET" && path == "/logout":
		h.webapp.WithSessionRequired(h.webapp.DeleteSession)(w, r)
	case method == "POST" && path == "/oauth/response/":
		h.webapp.PostOauthResponse(w, r)
	case method == "GET" && path == "/user":
		h.webapp.WithSessionRequired(h.webapp.WithAccountDetails(h.webapp.GetUserDetails))(w, r)
	case method == "GET" && path == "/healthz":
		h.webapp.HealthStatus(w, r)
	case method == "GET" && path == "/":
		h.webapp.WithAccountDetails(h.webapp.GetHome)(w, r)
	default:
		http.NotFound(w, r)
	}
}

// setPathValue simulates Go 1.22's http.Request.PathValue functionality
func (h *Handler) setPathValue(r *http.Request, key, value string) *http.Request {
	return helpers.WithPathValue(r, key, value)
}

// responseRecorder captures response data for conversion to API Gateway response
type responseRecorder struct {
	header     http.Header
	body       strings.Builder
	statusCode int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{
		header:     make(http.Header),
		statusCode: 200,
	}
}

func (r *responseRecorder) Header() http.Header {
	return r.header
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

// convertToAPIGatewayResponse converts response recorder to API Gateway response
func (h *Handler) convertToAPIGatewayResponse(recorder *responseRecorder) events.APIGatewayV2HTTPResponse {
	headers := make(map[string]string)
	multiValueHeaders := make(map[string][]string)

	// Copy headers from recorder
	for name, values := range recorder.header {
		if len(values) == 1 {
			headers[name] = values[0]
		} else if len(values) > 1 {
			multiValueHeaders[name] = values
		}
	}

	// Ensure CORS headers for API Gateway
	if _, exists := headers["Access-Control-Allow-Origin"]; !exists {
		headers["Access-Control-Allow-Origin"] = "*"
	}
	if _, exists := headers["Access-Control-Allow-Methods"]; !exists {
		headers["Access-Control-Allow-Methods"] = "GET, POST, PUT, DELETE, OPTIONS"
	}
	if _, exists := headers["Access-Control-Allow-Headers"]; !exists {
		headers["Access-Control-Allow-Headers"] = "Content-Type, Authorization"
	}

	response := events.APIGatewayV2HTTPResponse{
		StatusCode: recorder.statusCode,
		Headers:    headers,
		Body:       recorder.body.String(),
	}

	if len(multiValueHeaders) > 0 {
		response.MultiValueHeaders = multiValueHeaders
	}

	return response
}

// errorResponse creates an error response
func (h *Handler) errorResponse(statusCode int, message string) events.APIGatewayV2HTTPResponse {
	errorBody := map[string]string{
		"error": message,
	}
	body, _ := json.Marshal(errorBody)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: string(body),
	}
}
