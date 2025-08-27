package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
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

	log.Println("checking DB")
	if ok, err := wa.cache.Check(); !(ok && err == nil) {
		return fmt.Errorf("problem connecting to cache (ok=%v): %v", ok, err)
	}
	log.Println("connected to DB")

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
		Secure:   true,
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
		Secure:   true,
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

		cookie, err := r.Cookie(sessionCookieName)
		if err == http.ErrNoCookie {
			l.Println("login cookie not found")
			// There is no account to load, so we'll move on without account information loaded
			next(w, r)
			return
		}

		accountID := cookie.Value
		quota, err := wa.cache.GetOrFetch(fmt.Sprintf("quotas:%s", accountID), func() (string, error) {
			return "", fmt.Errorf("expected quota in quotas cache")
		})
		if err != nil {
			// It's weird for a logged in user to not have a quota. Log it, be generous, and set a new quota.
			// It may be a timing issue depending on when the underlying expires.
			log.Printf("expected a quota for user %s, but found nothing (not even 0)\n", accountID)
			qs := strconv.Itoa(maxQuota)
			wa.cache.SetNx(fmt.Sprintf("quotas:%s", accountID), qs, maxQuotaLifespanSeconds)
			quota = qs
		}

		email, err := wa.cache.GetOrFetch(fmt.Sprintf("accounts:%s", accountID), func() (string, error) {
			return "", fmt.Errorf("expected email in accounts cache")
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "unable to get email: %v", err)
			return

		}

		ctx := context.WithValue(context.WithValue(r.Context(), quotaContextName, quota), emailContextName, email)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (wa *Webapp) WithSufficientQuota(next http.HandlerFunc) http.HandlerFunc {
	return wa.WithAccountDetails(func(w http.ResponseWriter, r *http.Request) {
		var quota string

		if q, ok := r.Context().Value(quotaContextName).(string); ok {
			quota = q
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "quota not loaded in context")
			return
		}

		val, err := strconv.ParseInt(quota, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "unable to parse quota: %v", err)
			return
		}

		if val <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "the current account has insufficient quota")
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

	l.Println("checking cache")
	raw, err := wa.cache.GetOrFetch(fmt.Sprintf("recipes:raw:%s", u), func() (string, error) {
		l.Println("path miss")
		recipeUrl, err := url.PathUnescape(u)
		if err != nil {
			return "", fmt.Errorf("invalid URL encoding (%s): %v", u, err)
		}
		l.Println("path miss. fetching at: ", recipeUrl)
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
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to fetch raw: %v", err), http.StatusBadRequest)
		return
	}

	md, err := wa.cache.GetOrFetch(fmt.Sprintf("recipes:parsed:%s", u), func() (string, error) {
		return helpers.CreateMarkdownFromRaw(u, raw)
	})
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to get content from page: %v", err), http.StatusInternalServerError)
		return
	}

	summary, err := wa.cache.GetOrFetch(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
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
	l.Println("Checking cache for this input", k)
	if cached, err := wa.cache.Get(k); err == nil {
		l.Println("Returning cached result for key", k)
		l.Println(cached)
		w.Header().Add("Content-Type", "application/json")
		fmt.Fprint(w, cached)
		return
	}

	l.Println("cache miss. calling model")
	response, err := models.GeneratePairingSuggestionsV2(ctx, wa.model, wa.tools, input)
	if err != nil {
		l.Println("error from model")
		l.Println(err)
		helpers.SendJSONError(w, fmt.Errorf("error generating suggestions: %v", err), http.StatusInternalServerError)
		return
	}

	l.Println("model came back")
	_, err = models.ParseSuggestionsV2(response)
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("error generating suggestions: %v", err), http.StatusInternalServerError)
		return
	}

	// TODO take over final result caching from the prompt; just do it in code
	if err := wa.cache.Set(k, response); err != nil {
		l.Printf("error! couldn't write cache (key=%s): %v\n", k, err)
	}

	accountID := r.Context().Value(sessionContextName)
	if a, ok := accountID.(string); ok {
		l.Println("Decrementing quota for", accountID)
		wa.cache.Decr(sessionQuotaKey(a))
	} else {
		l.Println("Unable to look up account to decrement quota")
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
	log.Println("Handling GetRecipeWineSuggestions")

	u := getPathValue(r, "url")
	if u == "" {
		helpers.SendJSONError(w, fmt.Errorf("URL required"), http.StatusBadRequest)
		return
	}

	summary, err := wa.cache.GetOrFetch(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
		return "", fmt.Errorf("expected a summary to be generated before this call")
	})
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to load summary: %v", err), http.StatusInternalServerError)
		return
	}

	suggestions, err := wa.cache.GetOrFetch(fmt.Sprintf("recipes:suggestions-json:%s", u), func() (string, error) {
		// Only decrement quota if the user has a cache miss
		accountID := r.Context().Value(sessionContextName)
		if a, ok := accountID.(string); ok {
			wa.cache.Decr(sessionQuotaKey(a))
		} else {
			return "", fmt.Errorf("unexpected session context type")
		}

		return models.GeneratePairingSuggestions(ctx, wa.model, summary)
	})

	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to get wine suggestions from the model: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, suggestions)
}

// GetRecentSuggestions implements the route at
// "GET /recipes/suggestions/recent" and loads a sample of previously-cached recipe
// analyses to give the user a quick way to explore the app.
func (wa *Webapp) GetRecentSuggestions(w http.ResponseWriter, r *http.Request) {
	keys, err := wa.cache.GetKeys("recipes:suggestions-json:*")
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to scan for previous recipes: %v", err), http.StatusInternalServerError)
		return
	}

	var urls []string
	for _, k := range keys {
		u := recentSuggestionRx.FindString(k)
		urls = append(urls, u)
	}

	// TODO push sort and limit options into data layer
	rand.Shuffle(len(urls), func(i, j int) {
		urls[i], urls[j] = urls[j], urls[i]
	})

	var count = 3
	if len(urls) < count {
		count = len(urls)
	}

	out, err := json.Marshal(urls[0:count])
	if err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to encode URL suggestions: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, string(out))
}

func (wa *Webapp) DeleteSession(w http.ResponseWriter, r *http.Request) {
	accountID := r.Context().Value(sessionContextName)
	if err := wa.cache.Delete(fmt.Sprintf("sessions:%s", accountID)); err != nil {
		helpers.SendJSONError(w, fmt.Errorf("unable to destroy session: %v", err), http.StatusInternalServerError)
	}

	wa.deleteCookie(sessionCookieName, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (wa *Webapp) PostOauthResponse(w http.ResponseWriter, r *http.Request) {
	log.Println("handling oauth response")
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

	wa.cache.Set(fmt.Sprintf("accounts:%s", claims.AccountID), claims.Email)
	wa.cache.SetEx(fmt.Sprintf("sessions:%s", claims.AccountID), "", 60*60*24*7)

	// Set max quota if not already set
	wa.cache.SetNx(fmt.Sprintf("quotas:%s", claims.AccountID), strconv.Itoa(maxQuota), maxQuotaLifespanSeconds)

	wa.setCookie(sessionCookieName, claims.AccountID, w)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (wa *Webapp) HealthStatus(w http.ResponseWriter, r *http.Request) {
	if ok, err := wa.cache.Check(); !ok || err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to connect to data layer: %v", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}
