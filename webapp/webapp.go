package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	adapter "github.com/i2y/langchaingo-mcp-adapter"
	"github.com/mark3labs/mcp-go/client"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/server"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"

	"github.com/thedahv/wine-pairing-suggestions/cache"
	"github.com/thedahv/wine-pairing-suggestions/data"
	"github.com/thedahv/wine-pairing-suggestions/helpers"
	"github.com/thedahv/wine-pairing-suggestions/lambdahelpers"
	"github.com/thedahv/wine-pairing-suggestions/models"
)

//go:embed templates/**/*.html
var templates embed.FS

const templatesRoot = "templates"

const sessionCookieName = "wine-suggestions-session"

type contextKey string

const sessionContextName contextKey = "AccountId"
const quotaContextName contextKey = "quota"
const emailContextName contextKey = "email"
const dynamoAccountContextName contextKey = "dynamoAccount"
const maxQuota = 10
const maxQuotaLifespanSeconds = 60 * 60 * 24 * 7

var recentSuggestionRx *regexp.Regexp = regexp.MustCompile(`https?://\S+|www\.\S+`)

func sessionQuotaKey(accountID string) string {
	return fmt.Sprintf("quotas:%s", accountID)
}

// getPathValue extracts path values from request, supporting both Go 1.22 PathValue and context-based fallback
func getPathValue(r *http.Request, key string) string {
	// Try Go 1.22 PathValue first (this will work in regular HTTP server)
	if value := r.PathValue(key); value != "" {
		return value
	}

	// Fallback to context-based approach (for Lambda compatibility)
	return lambdahelpers.GetPathValue(r, key)
}

// Webapp contains handlers and functionality suited for implementing a wine suggestions web application.
// Construct a new Webapp with NewWebapp.
type Webapp struct {
	port           int
	tmpl           *template.Template
	cache          cache.Cacher
	cacheEnabled   bool // Feature flag to enable/disable cache operations
	dl             *data.DataLayer
	googleClientID string
	hostname       string
	model          llms.Model
	toolserver     *mcpserver.MCPServer
	toolclient     *mcpclient.Client
	tools          []tools.Tool
}

// Option configures the Webapp with various options
type Option func(*Webapp) error

// WithMemoryCache configures the Webapp to use an in-memory cache. It does not
// persist between restarts.
func WithMemoryCache() Option {
	return func(wa *Webapp) error {
		wa.cache = cache.NewMemory()
		return nil
	}
}

// WithRedisCache configures the Webapp to connect to a Redis server at the
// given host and port.
func WithRedisCache(host string, port int) Option {
	return func(wa *Webapp) error {
		wa.cache = cache.NewRedis(host, port)
		return nil
	}
}

func WithCache(cache cache.Cacher) Option {
	return func(wa *Webapp) error {
		wa.cache = cache
		return nil
	}
}

func WithDatabase(dl *data.DataLayer) Option {
	return func(wa *Webapp) error {
		wa.dl = dl
		return nil
	}
}

// WithGoogleClientID configures settings for Google OAuth.
func WithGoogleClientID(id string) Option {
	return func(wa *Webapp) error {
		wa.googleClientID = id
		return nil
	}
}

// WithHostname configures the protocol and hostname to configure OAuth redirects.
func WithHostname(hostname string) Option {
	return func(wa *Webapp) error {
		wa.hostname = hostname
		return nil
	}
}

// WithModel sets the model for the webapp, allowing clients to decide which
// model to use at startup.
func WithModel(model llms.Model, server *server.MCPServer) Option {
	return func(wa *Webapp) error {
		wa.model = model
		wa.toolserver = server

		client, err := client.NewInProcessClient(wa.toolserver)
		if err != nil {
			return fmt.Errorf("could not create MCP client: %v", err)
		}
		wa.toolclient = client
		a, err := adapter.New(client)
		if err != nil {
			return fmt.Errorf("unable to create adapter: %v", err)
		}

		if t, err := a.Tools(); err != nil {
			return fmt.Errorf("unable to create tools: %v", err)
		} else {
			wa.tools = t
		}

		return nil
	}
}

// NewWebapp builds a new Webapp configured and ready to listen to traffic on
// the given port. Call Start on a new webapp to begin receiving traffic.
func NewWebapp(port int, options ...Option) (*Webapp, error) {
	wa := &Webapp{
		port: port,
		tmpl: template.New(""),
	}

	if err := wa.buildTemplates(templatesRoot); err != nil {
		return nil, fmt.Errorf("unable to build templates: %v", err)
	}

	for _, option := range options {
		if err := option(wa); err != nil {
			return wa, fmt.Errorf("unable to apply option: %v", err)
		}
	}

	if wa.cache == nil {
		return wa, fmt.Errorf("no cache configured in options")
	}

	if wa.toolclient != nil {
		defer wa.toolclient.Close()
	}

	return wa, nil
}

// Start registers the route handlers on the web app and begins listening for traffic.
func (wa *Webapp) Start() error {
	log.Println("starting up...")
	ctx := context.Background()

	// Check if cache is enabled via environment variable (default: disabled)
	wa.cacheEnabled = os.Getenv("ENABLE_CACHE") == "true"
	if wa.cacheEnabled {
		log.Println("Cache feature flag ENABLED - cache will be used as performance layer")
	} else {
		log.Println("Cache feature flag DISABLED - DynamoDB will be primary data source")
	}

	// Only check cache health if cache is enabled
	if wa.cacheEnabled {
		log.Println("checking Cache")
		if ok, err := wa.cache.Check(); !(ok && err == nil) {
			return fmt.Errorf("problem connecting to cache (ok=%v): %v", ok, err)
		}
		log.Println("connected to Cache")
	}

	log.Println("Validating Database tables")
	if err := wa.dl.ValidateTables(ctx); err != nil {
		return fmt.Errorf("unable to validate tables: %v", err)
	}
	log.Println("Database tables validated")

	log.Println("registering routes...")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /recipes/summary/{url}", wa.WithSessionRequired(wa.WithSufficientQuota(wa.PostCreateRecipe)))
	mux.HandleFunc("GET /recipes/suggestions/recent", wa.WithSessionRequired(wa.GetRecentSuggestions))
	mux.HandleFunc("GET /recipes/suggestions/{url}", wa.WithSessionRequired(wa.WithSufficientQuota(wa.GetRecipeWineSuggestions)))
	mux.HandleFunc("POST /recipes/suggestionsV2/", wa.WithSessionRequired(wa.WithSufficientQuota(wa.GetRecipeWineSuggestionsV2)))
	mux.HandleFunc("GET /logout", wa.WithSessionRequired(wa.DeleteSession))
	mux.HandleFunc("POST /oauth/response/", wa.PostOauthResponse)
	mux.HandleFunc("GET /user", wa.WithSessionRequired(wa.WithAccountDetails(wa.GetUserDetails)))
	mux.HandleFunc("GET /healthz", wa.HealthStatus)
	mux.HandleFunc("GET /", wa.WithAccountDetails(wa.GetHome))

	log.Printf("listening on :%d\n", wa.port)
	return http.ListenAndServe(fmt.Sprintf(":%d", wa.port), mux)
}

func (wa *Webapp) getCookie(name string, r *http.Request) (*http.Cookie, error) {
	return r.Cookie(name)
}

func (wa *Webapp) setCookie(name string, val string, w http.ResponseWriter) {
	cookie := http.Cookie{
		Name:     name,
		Value:    val,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(wa.hostname, "https"),
		SameSite: http.SameSiteLaxMode,
	}

	http.SetCookie(w, &cookie)
}

func (wa *Webapp) deleteCookie(name string, w http.ResponseWriter) {
	cookie := http.Cookie{
		Name:     name,
		Value:    "",
		Expires:  time.Unix(0, 0),
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(wa.hostname, "https"),
		SameSite: http.SameSiteLaxMode,
	}

	http.SetCookie(w, &cookie)
}

func (wa *Webapp) WithSessionRequired(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO encode the account ID somehow so it's not just bare in the cookie
		cookie, err := r.Cookie(sessionCookieName)
		if err == http.ErrNoCookie {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "session required")
			return
		}

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "unable to read session cookie: %v", err)
			return
		}

		ctx := context.WithValue(r.Context(), sessionContextName, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (wa *Webapp) WithAccountDetails(next http.HandlerFunc) http.HandlerFunc {
	fmt.Println("creating withAccountDetails middleware")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := log.New(log.Default().Writer(), "withAccountDetails", log.Default().Flags())

		l.Println("Cookies on request:")
		for _, c := range r.Cookies() {
			l.Printf(" - %s=%s\n", c.Name, c.Value)
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err == http.ErrNoCookie {
			l.Println("login cookie not found")
			// There is no account to load, so we'll move on without account information loaded
			next(w, r)
			return
		}

		accountID := cookie.Value

		// --- DynamoDB is PRIMARY source of truth ---
		l.Printf("[DB] Fetching account details from DynamoDB (AccountID=%s)\n", accountID)
		dynamoAccount, err := wa.dl.GetAccountByID(r.Context(), accountID)
		if err != nil {
			l.Printf("[DB] Failed to get account from DynamoDB: %v\n", err)
		} else {
			l.Printf("[DB] Loaded from DynamoDB: Email=%s, Quota=%d\n", dynamoAccount.Email, dynamoAccount.Quota)
		}

		// Use DB values for context (primary source)
		var email string
		var quota string
		if err == nil {
			email = dynamoAccount.Email
			quota = strconv.Itoa(dynamoAccount.Quota)
		}

		// --- Cache as OPTIONAL performance layer ---
		if wa.cacheEnabled {
			l.Println("[CACHE] Cache enabled - fetching from cache as backup")
			cacheQuota, err := wa.cache.GetOrFetch(fmt.Sprintf("quotas:%s", accountID), func() (string, error) {
				return "", fmt.Errorf("expected quota in quotas cache")
			})
			if err != nil {
				l.Printf("[CACHE] Quota not found for user %s\n", accountID)
				// If we have DB data, backfill cache
				if quota != "" {
					l.Println("[CACHE] Backfilling quota from DB to cache")
					wa.cache.SetNx(fmt.Sprintf("quotas:%s", accountID), quota, maxQuotaLifespanSeconds)
				}
			} else {
				l.Printf("[CACHE] Loaded quota from cache: %s\n", cacheQuota)
				// Use cache value if DB didn't return one
				if quota == "" {
					quota = cacheQuota
				}
			}

			cacheEmail, err := wa.cache.GetOrFetch(fmt.Sprintf("accounts:%s", accountID), func() (string, error) {
				return "", fmt.Errorf("expected email in accounts cache")
			})
			if err != nil {
				l.Printf("[CACHE] Email not found for user %s\n", accountID)
				// If we have DB data, backfill cache
				if email != "" {
					l.Println("[CACHE] Backfilling email from DB to cache")
					wa.cache.Set(fmt.Sprintf("accounts:%s", accountID), email)
				}
			} else {
				l.Printf("[CACHE] Loaded email from cache: %s\n", cacheEmail)
				// Use cache value if DB didn't return one
				if email == "" {
					email = cacheEmail
				}
			}
		}

		ctx := context.WithValue(
			context.WithValue(
				context.WithValue(
					r.Context(), quotaContextName, quota,
				),
				emailContextName, email,
			),
			dynamoAccountContextName, dynamoAccount,
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (wa *Webapp) WithSufficientQuota(next http.HandlerFunc) http.HandlerFunc {
	return wa.WithAccountDetails(func(w http.ResponseWriter, r *http.Request) {
		l := log.New(log.Default().Writer(), "[WithSufficientQuota]", log.Default().Flags())

		// --- Use quota from context (loaded from DB or cache in WithAccountDetails) ---
		var quota int64

		// Prefer DB quota if available
		if a, ok := r.Context().Value(dynamoAccountContextName).(data.Account); ok && a.ID != "" {
			quota = int64(a.Quota)
			l.Printf("[DB] Using quota from DynamoDB: %d\n", quota)
		} else {
			// Fall back to context quota (may be from cache if enabled)
			if q, ok := r.Context().Value(quotaContextName).(string); ok && q != "" {
				var err error
				quota, err = strconv.ParseInt(q, 10, 64)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, "unable to parse quota: %v", err)
					return
				}
				l.Printf("Using quota from context: %d\n", quota)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, "quota not loaded in context")
				return
			}
		}

		if quota <= 0 {
			l.Printf("Account has insufficient quota (%d)\n", quota)
			w.WriteHeader(http.StatusBadRequest)
			helpers.SendJSONError(w, fmt.Errorf("the current account has insufficient quota"), http.StatusBadRequest)
			return
		}

		next(w, r)
	})
}

// buildTemplates finds, compiles, and registers all view templates for this
// webapp for use in route handlers, throwing an error if anything fails to
// compile. Templates are named by their file path (including extension) within
// the templates folder. For example, a template at
// "webapp/templates/folder/template.html" will be called
// "folder/template.html". Use wa.tmpl.Lookup("folder/template.html") to use it
// in a handler.
func (wa *Webapp) buildTemplates(parent string) error {
	entries, err := templates.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("embed ReadDir error on path=%s: %v", parent, err)
	}

	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			err := wa.buildTemplates(path.Join(parent, n))
			if err != nil {
				return err
			}
		} else {
			p := path.Join(parent, n)
			if parent == "template" {
				p = n
			}

			contents, err := templates.ReadFile(p)
			if err != nil {
				return fmt.Errorf("embed ReadFile error on path=%s: %v", p, err)
			}
			name := strings.Replace(p, "templates"+string(filepath.Separator), "", 1)
			template.Must(wa.tmpl.New(name).Parse(string(contents)))
		}
	}

	return nil
}

// GetUserDetails fetches the latest information about the currently logged in user
func (wa *Webapp) GetUserDetails(w http.ResponseWriter, r *http.Request) {
	var quota, email string
	if q, ok := r.Context().Value(quotaContextName).(string); ok {
		quota = q
	}
	if e, ok := r.Context().Value(emailContextName).(string); ok {
		email = e
	}

	data := struct {
		Email string `json:"email"`
		Quota string `json:"quota"`
	}{
		Email: email,
		Quota: quota,
	}

	out, _ := json.Marshal(data)
	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, string(out))
}

// GetHome implements home route "GET /" for the web app, serving the home page
// and initializing the app.
func (wa *Webapp) GetHome(w http.ResponseWriter, r *http.Request) {
	var quota, email string
	if q, ok := r.Context().Value(quotaContextName).(string); ok {
		quota = q
	}
	if e, ok := r.Context().Value(emailContextName).(string); ok {
		email = e
	}

	data := struct {
		Email          string
		Quota          string
		GoogleClientID string
		Hostname       string
	}{
		Email:          email,
		Quota:          quota,
		GoogleClientID: wa.googleClientID,
		Hostname:       wa.hostname,
	}

	// The template will render an inline login screen if there isn't an active session
	t := wa.tmpl.Lookup("pages/home.html")
	if t == nil {
		// Set a 500 status on the response
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to lookup template: %s", "pages/home.html")
		return
	}

	w.Header().Add("Content-Type", "text/html")
	if err := t.Execute(w, data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to render template: %v", err)
	}
}

// PostCreateRecipe implements a route at "POST /recipes/summary" to create a
// new analysis of a recipe indicated by the url field in the form submission.
// Returns an HTML partial with the summarized analysis of the given recipe.
// Note: This endpoint uses cache if enabled, but does NOT store in DynamoDB
// (raw/parsed content is out of scope for RecipePairing model).
func (wa *Webapp) PostCreateRecipe(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	log.Println("Handling PostCreateRecipe")
	u := getPathValue(r, "url")
	log.Println("recipe is", u)
	if u == "" {
		helpers.SendJSONError(w, fmt.Errorf("URL required"), http.StatusBadRequest)
		return
	}
	l := log.New(log.Default().Writer(), fmt.Sprintf("[PostCreateRecipe %s]", u[0:15]), log.Default().Flags())

	// Fetch raw HTML (use cache if enabled)
	var raw string
	var err error
	if wa.cacheEnabled {
		l.Println("[CACHE] Cache enabled - checking for raw HTML")
		raw, err = wa.cache.GetOrFetch(fmt.Sprintf("recipes:raw:%s", u), func() (string, error) {
			l.Println("[CACHE] Cache miss - fetching raw HTML")
			recipeUrl, err := url.PathUnescape(u)
			if err != nil {
				return "", fmt.Errorf("invalid URL encoding (%s): %v", u, err)
			}
			l.Println("Fetching from URL:", recipeUrl)
			raw, err := helpers.FetchRawFromURL(recipeUrl)
			if err != nil {
				return "", err
			}
			defer raw.Close()
			contents, err := io.ReadAll(raw)
			if err != nil {
				return "", fmt.Errorf("unable to read website contents")
			}
			return string(contents), nil
		})
	} else {
		l.Println("Cache disabled - fetching raw HTML directly")
		recipeUrl, err := url.PathUnescape(u)
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("invalid URL encoding (%s): %v", u, err), http.StatusBadRequest)
			return
		}
		rawReader, err := helpers.FetchRawFromURL(recipeUrl)
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("unable to fetch raw: %v", err), http.StatusBadRequest)
			return
		}
		defer rawReader.Close()
		contents, err := io.ReadAll(rawReader)
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("unable to read website contents: %v", err), http.StatusInternalServerError)
			return
		}
		raw = string(contents)
	}
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to fetch raw: %v", err), http.StatusBadRequest)
		return
	}

	// Parse to markdown (use cache if enabled)
	var md string
	if wa.cacheEnabled {
		l.Println("[CACHE] Cache enabled - checking for parsed markdown")
		md, err = wa.cache.GetOrFetch(fmt.Sprintf("recipes:parsed:%s", u), func() (string, error) {
			l.Println("[CACHE] Cache miss - parsing to markdown")
			return helpers.CreateMarkdownFromRaw(u, raw)
		})
	} else {
		l.Println("Cache disabled - parsing to markdown directly")
		md, err = helpers.CreateMarkdownFromRaw(u, raw)
	}
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to get content from page: %v", err), http.StatusInternalServerError)
		return
	}

	// Summarize recipe (use cache if enabled)
	var summary string
	if wa.cacheEnabled {
		l.Println("[CACHE] Cache enabled - checking for summary")
		summary, err = wa.cache.GetOrFetch(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
			l.Println("[CACHE] Cache miss - generating summary")
			out, err := models.SummarizeRecipe(ctx, wa.model, md)
			if err != nil {
				return "", fmt.Errorf("unable to get summary prompt response: %v", err)
			}
			parsed, err := models.ParseSummary(out)
			if err != nil {
				return "", fmt.Errorf("unable to parse summary prompt response: %v", err)
			}
			if !parsed.Ok {
				return "", fmt.Errorf("model aborted recipe summary: %s", parsed.AbortReason)
			}
			return parsed.Summary, nil
		})
	} else {
		l.Println("Cache disabled - generating summary directly")
		out, err := models.SummarizeRecipe(ctx, wa.model, md)
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("unable to get summary prompt response: %v", err), http.StatusInternalServerError)
			return
		}
		parsed, err := models.ParseSummary(out)
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("unable to parse summary prompt response: %v", err), http.StatusInternalServerError)
			return
		}
		if !parsed.Ok {
			helpers.SendJSONError(w, fmt.Errorf("model aborted recipe summary: %s", parsed.AbortReason), http.StatusBadRequest)
			return
		}
		summary = parsed.Summary
	}
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to summarize recipe contents: %v", err), http.StatusInternalServerError)
		return
	}

	tmp := struct {
		Summary string `json:"summary"`
	}{
		Summary: summary,
	}

	out, err := json.Marshal(tmp)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to render summary JSON: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, string(out))
}

func getCacheKeyForInput(input string) string {
	// test for URL. use content hash otherwise.
	if matches := recentSuggestionRx.FindString(input); matches != "" {
		// Input is a URL, so use it directly as cache key
		return fmt.Sprintf("recipes:suggestions-json:%s", matches)
	}

	// Input is not a URL, hash the content for cache key
	hash := helpers.HashContent(input)
	return fmt.Sprintf("recipes:suggestions-json:content:%s", hash)
}

// getPairingIDAndType determines the pairing ID and type from input.
// Returns the ID (URL or content hash) and the corresponding PairingType.
func getPairingIDAndType(input string) (string, data.PairingType) {
	if matches := recentSuggestionRx.FindString(input); matches != "" {
		return matches, data.PairingTypeURL
	}
	hash := helpers.HashContent(input)
	return hash, data.PairingTypeContentHash
}

// convertToDataSuggestions converts models.Suggestion slice to data.Suggestion slice
func convertToDataSuggestions(modelSuggestions []models.Suggestion) []data.Suggestion {
	dataSuggestions := make([]data.Suggestion, len(modelSuggestions))
	for i, ms := range modelSuggestions {
		dataSuggestions[i] = data.Suggestion{
			Style:       ms.Style,
			Region:      ms.Region,
			Description: ms.Description,
			PairingNote: ms.PairingNote,
		}
	}
	return dataSuggestions
}

// convertFromDataSuggestions converts data.Suggestion slice to models.Suggestion slice
func convertFromDataSuggestions(dataSuggestions []data.Suggestion) []models.Suggestion {
	modelSuggestions := make([]models.Suggestion, len(dataSuggestions))
	for i, ds := range dataSuggestions {
		modelSuggestions[i] = models.Suggestion{
			Style:       ds.Style,
			Region:      ds.Region,
			Description: ds.Description,
			PairingNote: ds.PairingNote,
		}
	}
	return modelSuggestions
}

// reconstructSuggestionsJSON converts a data.Suggestion slice to JSON string
func reconstructSuggestionsJSON(suggestions []data.Suggestion) (string, error) {
	modelSuggestions := convertFromDataSuggestions(suggestions)
	jsonBytes, err := json.Marshal(modelSuggestions)
	if err != nil {
		return "", fmt.Errorf("failed to marshal suggestions to JSON: %w", err)
	}
	return string(jsonBytes), nil
}

// reconstructSuggestionsV2JSON converts RecipePairing to SuggestionsResponse JSON string
func reconstructSuggestionsV2JSON(pairing data.RecipePairing) (string, error) {
	response := models.SuggestionsResponse{
		Suggestions: convertFromDataSuggestions(pairing.Suggestions),
		Summary:     pairing.Summary,
	}
	jsonBytes, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal suggestions response to JSON: %w", err)
	}
	return string(jsonBytes), nil
}

// GetRecipeWineSuggestionsV2 implements the route at
// "GET /recipes/suggestionsV2/{url}". Note that "POST /recipes" MUST be called first.
// Otherwise, this route calls a bad request error since the recipe summary
// hasn't been cached yet. This introduces a stateful dependency, but it
// minimizes the need to pass the summary to this endpoint in the request.
func (wa *Webapp) GetRecipeWineSuggestionsV2(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	l := log.New(log.Default().Writer(), "[GetRecipeWineSuggestionsV2] ", log.Default().Flags())
	l.Println("Handling GetRecipeWineSuggestionsV2")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to read request: %v", err), http.StatusInternalServerError)
		return
	}

	input := strings.TrimSpace(string(body))

	if input == "" {
		helpers.SendJSONError(w, fmt.Errorf("input cannot be empty"), http.StatusBadRequest)
		return
	}

	k := getCacheKeyForInput(input)
	pairingID, pairingType := getPairingIDAndType(input)

	// PRIMARY: Try DynamoDB first (source of truth)
	l.Printf("[DB] Checking DynamoDB for pairing ID: %s (type: %s)\n", pairingID, pairingType)
	if pairing, err := wa.dl.GetRecipePairing(ctx, pairingID); err == nil {
		l.Printf("[DB] Found pairing in DynamoDB (created: %s)\n", pairing.DateCreated)

		// Reconstruct JSON response from DynamoDB data
		responseJSON, err := reconstructSuggestionsV2JSON(pairing)
		if err != nil {
			l.Printf("[DB] Error reconstructing JSON from DynamoDB pairing: %v\n", err)
		} else {
			// Backfill cache if enabled
			if wa.cacheEnabled {
				l.Println("[CACHE] Cache enabled - backfilling cache from DynamoDB result")
				if err := wa.cache.Set(k, responseJSON); err != nil {
					l.Printf("[CACHE] Error backfilling cache: %v\n", err)
				}
			}

			w.Header().Add("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
			return
		}
	} else if !errors.Is(err, data.ErrNotFound) {
		l.Printf("[DB] Error querying DynamoDB: %v\n", err)
	} else {
		l.Println("[DB] Pairing not found in DynamoDB")
	}

	// OPTIONAL: Try cache if enabled and DB missed
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - checking cache for key: %s\n", k)
		if cached, err := wa.cache.Get(k); err == nil {
			l.Println("[CACHE] Cache hit, returning cached result")
			w.Header().Add("Content-Type", "application/json")
			fmt.Fprint(w, cached)
			return
		}
		l.Println("[CACHE] Cache miss")
	}

	// Both systems missed - generate new content
	l.Println("Generating new suggestions with model")
	response, err := models.GeneratePairingSuggestionsV2(ctx, wa.model, wa.tools, input)
	if err != nil {
		l.Printf("Error from model: %v\n", err)
		helpers.SendJSONError(w, fmt.Errorf("error generating suggestions: %v", err), http.StatusInternalServerError)
		return
	}

	l.Println("Model response received")
	if os.Getenv("LOG_LEVEL") == "TRACE" {
		l.Println(response)
	}

	// Parse the response to extract suggestions and summary
	parsed, err := models.ParseSuggestionsV2(response)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("error generating suggestions: %v", err), http.StatusInternalServerError)
		return
	}

	// PRIMARY: Store in DynamoDB
	dataSuggestions := convertToDataSuggestions(parsed.Suggestions)
	l.Printf("[DB] Storing pairing in DynamoDB (ID: %s, type: %s)\n", pairingID, pairingType)
	if _, err := wa.dl.CreateRecipePairing(ctx, pairingID, pairingType, parsed.Summary, dataSuggestions); err != nil {
		l.Printf("[DB] Error storing in DynamoDB: %v\n", err)
	}

	// OPTIONAL: Store in cache if enabled
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - storing suggestions in cache (key: %s)\n", k)
		if err := wa.cache.Set(k, response); err != nil {
			l.Printf("[CACHE] Error storing in cache: %v\n", err)
		}
	}

	accountID := r.Context().Value(sessionContextName)
	if a, ok := accountID.(string); ok {
		// PRIMARY: Decrement quota in DynamoDB
		l.Printf("[DB] Decrementing quota for account %s in DynamoDB\n", a)
		if err := wa.dl.DecrementAccountQuota(ctx, a); err != nil {
			l.Printf("[DB] Error decrementing quota in DynamoDB: %v\n", err)
		}

		// OPTIONAL: Decrement in cache if enabled
		if wa.cacheEnabled {
			l.Printf("[CACHE] Cache enabled - decrementing quota for account %s in cache\n", a)
			if err := wa.cache.Decr(sessionQuotaKey(a)); err != nil {
				l.Printf("[CACHE] Error decrementing quota in cache: %v\n", err)
			}
		}
	} else {
		l.Println("Unable to look up account ID from context to decrement quota")
	}

	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, response)
}

// GetRecipeWineSuggestions implements the route at
// "GET /recipes/suggestions/{url}". Note that "POST /recipes" MUST be called first.
// Otherwise, this route calls a bad request error since the recipe summary
// hasn't been cached yet. This introduces a stateful dependency, but it
// minimizes the need to pass the summary to this endpoint in the request.
func (wa *Webapp) GetRecipeWineSuggestions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	l := log.New(log.Default().Writer(), "[GetRecipeWineSuggestions]", log.Default().Flags())
	l.Println("Handling GetRecipeWineSuggestions")

	u := getPathValue(r, "url")
	if u == "" {
		helpers.SendJSONError(w, fmt.Errorf("URL required"), http.StatusBadRequest)
		return
	}

	cacheKey := fmt.Sprintf("recipes:suggestions-json:%s", u)
	pairingID := u // For this endpoint, the pairing ID is the URL itself
	pairingType := data.PairingTypeURL

	// PRIMARY: Try DynamoDB first (source of truth)
	l.Printf("[DB] Checking DynamoDB for pairing ID: %s\n", pairingID)
	if pairing, err := wa.dl.GetRecipePairing(ctx, pairingID); err == nil {
		l.Printf("[DB] Found pairing in DynamoDB (created: %s)\n", pairing.DateCreated)

		// Reconstruct JSON response from DynamoDB data
		suggestionsJSON, err := reconstructSuggestionsJSON(pairing.Suggestions)
		if err != nil {
			l.Printf("[DB] Error reconstructing JSON from DynamoDB pairing: %v\n", err)
		} else {
			// Backfill cache if enabled
			if wa.cacheEnabled {
				l.Println("[CACHE] Cache enabled - backfilling cache from DynamoDB result")
				if err := wa.cache.Set(cacheKey, suggestionsJSON); err != nil {
					l.Printf("[CACHE] Error backfilling cache: %v\n", err)
				}
			}

			w.Header().Add("Content-Type", "application/json")
			fmt.Fprint(w, suggestionsJSON)
			return
		}
	} else if !errors.Is(err, data.ErrNotFound) {
		l.Printf("[DB] Error querying DynamoDB: %v\n", err)
	} else {
		l.Println("[DB] Pairing not found in DynamoDB")
	}

	// OPTIONAL: Try cache if enabled and DB missed
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - checking cache for key: %s\n", cacheKey)
		if cached, err := wa.cache.Get(cacheKey); err == nil {
			l.Println("[CACHE] Cache hit, returning cached suggestions")
			w.Header().Add("Content-Type", "application/json")
			fmt.Fprint(w, cached)
			return
		}
		l.Println("[CACHE] Cache miss")
	}

	// Need to generate new - get summary first
	var summary string
	if wa.cacheEnabled {
		// Try to get summary from cache (expected from prior PostCreateRecipe call)
		l.Println("[CACHE] Cache enabled - fetching summary from cache")
		var err error
		summary, err = wa.cache.GetOrFetch(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
			return "", fmt.Errorf("expected a summary to be generated before this call")
		})
		if err != nil {
			helpers.SendJSONError(w, fmt.Errorf("unable to load summary: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// Cache disabled - need summary for generation but don't have it
		// This endpoint requires PostCreateRecipe to be called first, which caches the summary
		// Without cache, we don't have the summary, so we can't proceed
		helpers.SendJSONError(w, fmt.Errorf("summary not available - this endpoint requires cache or prior pairing in DB"), http.StatusInternalServerError)
		return
	}

	// Both systems missed - generate new content
	accountID := r.Context().Value(sessionContextName)
	a, ok := accountID.(string)
	if !ok {
		helpers.SendJSONError(w, fmt.Errorf("unable to get account ID from context"), http.StatusInternalServerError)
		return
	}

	// PRIMARY: Decrement quota in DynamoDB before generating
	l.Printf("[DB] Decrementing quota for account %s in DynamoDB\n", a)
	if err := wa.dl.DecrementAccountQuota(ctx, a); err != nil {
		l.Printf("[DB] Error decrementing quota: %v\n", err)
	}

	// OPTIONAL: Decrement in cache if enabled
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - decrementing quota for account %s in cache\n", a)
		if err := wa.cache.Decr(sessionQuotaKey(a)); err != nil {
			l.Printf("[CACHE] Error decrementing quota: %v\n", err)
		}
	}

	// Generate suggestions using LLM
	l.Println("Generating new suggestions with model")
	suggestionsJSON, err := models.GeneratePairingSuggestions(ctx, wa.model, summary)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to get wine suggestions from the model: %v", err), http.StatusInternalServerError)
		return
	}

	// Parse suggestions to store in DynamoDB
	modelSuggestions, err := models.ParseSuggestions(suggestionsJSON)
	if err != nil {
		l.Printf("Warning: unable to parse suggestions for DynamoDB storage: %v\n", err)
		// Still return the response even if we can't parse for storage
		w.Header().Add("Content-Type", "application/json")
		fmt.Fprint(w, suggestionsJSON)
		return
	}

	// PRIMARY: Store in DynamoDB
	dataSuggestions := convertToDataSuggestions(modelSuggestions)
	l.Printf("[DB] Storing pairing in DynamoDB (ID: %s, type: %s)\n", pairingID, pairingType)
	if _, err := wa.dl.CreateRecipePairing(ctx, pairingID, pairingType, summary, dataSuggestions); err != nil {
		l.Printf("[DB] Error storing in DynamoDB: %v\n", err)
	}

	// OPTIONAL: Store in cache if enabled
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - storing suggestions in cache (key: %s)\n", cacheKey)
		if err := wa.cache.Set(cacheKey, suggestionsJSON); err != nil {
			l.Printf("[CACHE] Error storing in cache: %v\n", err)
		}
	}

	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, suggestionsJSON)
}

// GetRecentSuggestions implements the route at
// "GET /recipes/suggestions/recent" and loads a sample of previously-cached recipe
// analyses to give the user a quick way to explore the app.
func (wa *Webapp) GetRecentSuggestions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	l := log.New(log.Default().Writer(), "[GetRecentSuggestions]", log.Default().Flags())

	// PRIMARY: Query DynamoDB for recent URL-based pairings
	l.Println("[DB] Querying DynamoDB for recent URL pairings")
	var dbURLs []string
	if ids, err := wa.dl.GetRecentRecipePairingIDs(ctx, data.PairingTypeURL, 20); err == nil {
		dbURLs = ids
		l.Printf("[DB] Found %d URL pairings in DynamoDB\n", len(dbURLs))
	} else {
		l.Printf("[DB] Error querying DynamoDB: %v\n", err)
	}

	// OPTIONAL: Query cache if enabled
	var cacheURLs []string
	if wa.cacheEnabled {
		l.Println("[CACHE] Cache enabled - scanning cache for recipe suggestions")
		if keys, err := wa.cache.GetKeys("recipes:suggestions-json:*"); err == nil {
			for _, k := range keys {
				if u := recentSuggestionRx.FindString(k); u != "" {
					cacheURLs = append(cacheURLs, u)
				}
			}
			l.Printf("[CACHE] Found %d suggestions in cache\n", len(cacheURLs))
		} else {
			l.Printf("[CACHE] Error scanning cache: %v\n", err)
		}
	}

	// Combine and deduplicate results
	urlSet := make(map[string]bool)
	for _, u := range dbURLs {
		if u != "" {
			urlSet[u] = true
		}
	}
	for _, u := range cacheURLs {
		if u != "" {
			urlSet[u] = true
		}
	}

	// Convert set to slice
	var allURLs []string
	for u := range urlSet {
		allURLs = append(allURLs, u)
	}

	l.Printf("Combined results: %d unique URLs from both systems\n", len(allURLs))

	// Shuffle and select random sample
	rand.Shuffle(len(allURLs), func(i, j int) {
		allURLs[i], allURLs[j] = allURLs[j], allURLs[i]
	})

	var count = 3
	if len(allURLs) < count {
		count = len(allURLs)
	}

	var result []string
	if count > 0 {
		result = allURLs[0:count]
	} else {
		result = []string{}
	}

	out, err := json.Marshal(result)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to encode URL suggestions: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, string(out))
}

func (wa *Webapp) DeleteSession(w http.ResponseWriter, r *http.Request) {
	accountID := r.Context().Value(sessionContextName)

	// Only delete from cache if cache is enabled
	if wa.cacheEnabled {
		if err := wa.cache.Delete(fmt.Sprintf("sessions:%s", accountID)); err != nil {
			log.Printf("[CACHE] Error deleting session from cache: %v\n", err)
			// Don't fail the request - cookie deletion is what matters
		}
	}

	wa.deleteCookie(sessionCookieName, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (wa *Webapp) PostOauthResponse(w http.ResponseWriter, r *http.Request) {
	l := log.New(log.Default().Writer(), "[PostOauthResponse]", log.Default().Flags())
	ctx := r.Context()
	l.Println("Handling oauth response")
	if err := r.ParseForm(); err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to parse request form: %v", err), http.StatusBadRequest)
		return
	}

	tokenParts := r.Form["g_csrf_token"]
	if len(tokenParts) == 0 {
		helpers.SendJSONError(w, fmt.Errorf("expected csrf cookies, got %v", r.Form["g_csrf_token"]), http.StatusBadRequest)
		return
	}

	csrfToken := tokenParts[0]
	csrfCookie, err := wa.getCookie("g_csrf_token", r)

	if err == http.ErrNoCookie {
		helpers.SendJSONError(w, fmt.Errorf("no csrf cookie set"), http.StatusBadRequest)
		return
	}
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to get csrf cookie: %v", err), http.StatusInternalServerError)
		return
	}
	if csrfToken != csrfCookie.Value {
		helpers.SendJSONError(w, fmt.Errorf("failed to verify double submit cookie"), http.StatusBadRequest)
		return
	}

	expectedMethod := "RS256"
	secret, err := helpers.GetGoogleJWTToken(expectedMethod)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to fetch latest Google certs: %v", err), http.StatusBadRequest)
		return
	}

	token := r.Form["credential"][0]
	parsed, err := jwt.ParseWithClaims(token, &helpers.Claims{}, func(t *jwt.Token) (interface{}, error) {
		if alg := t.Header["alg"]; alg != expectedMethod {
			return nil, fmt.Errorf("expected signing method %s, got %s", expectedMethod, alg)
		}

		return secret, nil
	})

	claims, ok := parsed.Claims.(*helpers.Claims)
	if !ok {
		helpers.SendJSONError(w, fmt.Errorf("unable to parse response as Google JWT"), http.StatusInternalServerError)
	}

	// --- PRIMARY: Create/update account in DynamoDB ---
	l.Printf("[DB] Creating/updating account in DynamoDB for AccountID: %s (email=%s)\n", claims.AccountID, claims.Email)
	if _, err := wa.dl.CreateAccount(ctx, claims.AccountID, claims.Email); err != nil {
		l.Printf("[DB] Error creating account in DynamoDB: %v\n", err)
		// This is now critical since DB is primary - but we'll continue for backwards compatibility
	}

	// --- OPTIONAL: Store in cache if enabled ---
	if wa.cacheEnabled {
		l.Printf("[CACHE] Cache enabled - setting account details in cache for AccountID: %s\n", claims.AccountID)
		if err := wa.cache.Set(fmt.Sprintf("accounts:%s", claims.AccountID), claims.Email); err != nil {
			l.Printf("[CACHE] Error setting account email in cache: %v\n", err)
		}
		if err := wa.cache.SetEx(fmt.Sprintf("sessions:%s", claims.AccountID), "", 60*60*24*7); err != nil {
			l.Printf("[CACHE] Error setting session in cache: %v\n", err)
		}
		if err := wa.cache.SetNx(fmt.Sprintf("quotas:%s", claims.AccountID), strconv.Itoa(maxQuota), maxQuotaLifespanSeconds); err != nil {
			l.Printf("[CACHE] Error setting quota in cache: %v\n", err)
		}
	}

	l.Println("Setting session cookie and redirecting")
	wa.setCookie(sessionCookieName, claims.AccountID, w)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (wa *Webapp) HealthStatus(w http.ResponseWriter, r *http.Request) {
	// Check cache only if enabled
	if wa.cacheEnabled {
		if ok, err := wa.cache.Check(); !ok || err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "unable to connect to cache: %v", err)
			return
		}
	}

	// Always check DynamoDB as it's the primary data source
	// A simple table list check verifies DB connectivity
	ctx := context.Background()
	if _, err := wa.dl.GetRecentRecipePairingIDs(ctx, data.PairingTypeURL, 1); err != nil && !errors.Is(err, data.ErrNotFound) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to connect to database: %v", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}
