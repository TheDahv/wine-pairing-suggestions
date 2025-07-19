package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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

	"github.com/thedahv/wine-pairing-suggestions/cache"
	"github.com/thedahv/wine-pairing-suggestions/helpers"
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

// Webapp contains handlers and functionality suited for implementing a wine suggestions web application.
// Construct a new Webapp with NewWebapp.
type Webapp struct {
	port           int
	tmpl           *template.Template
	cache          cache.Cacher
	googleClientID string
	hostname       string
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

	return wa, nil
}

// Start registers the route handlers on the web app and begins listening for traffic.
func (wa *Webapp) Start() error {
	fmt.Println("starting up...")

	fmt.Println("checking DB")
	if ok, err := wa.cache.Check(); !(ok && err == nil) {
		return fmt.Errorf("problem connecting to cache (ok=%v): %v", ok, err)
	}
	fmt.Println("connected to DB")

	fmt.Println("registering routes...")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /recipes/summary/{url}", wa.withSessionRequired(wa.withSufficientQuota(wa.PostCreateRecipe)))
	mux.HandleFunc("GET /recipes/suggestions/recent", wa.withSessionRequired(wa.GetRecentSuggestions))
	mux.HandleFunc("GET /recipes/suggestions/{url}", wa.withSessionRequired(wa.withSufficientQuota(wa.GetRecipeWineSuggestions)))
	mux.HandleFunc("GET /logout", wa.withSessionRequired(wa.DeleteSession))
	mux.HandleFunc("GET /login", wa.GetLogin)
	mux.HandleFunc("POST /oauth/response/", wa.PostOauthResponse)
	mux.HandleFunc("GET /healthz", wa.HealthStatus)
	mux.HandleFunc("GET /", wa.withRedirectForLogin(wa.withAccountDetails(wa.GetHome)))

	fmt.Printf("listening on :%d\n", wa.port)
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

func (wa *Webapp) withSessionRequired(next http.HandlerFunc) http.HandlerFunc {
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

func (wa *Webapp) withRedirectForLogin(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountId := r.Context().Value(sessionContextName)
		if accountId == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (wa *Webapp) withAccountDetails(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err == http.ErrNoCookie {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		accountID := cookie.Value

		quota, err := wa.cache.Get(fmt.Sprintf("quotas:%s", accountID), func() (string, error) {
			return "", fmt.Errorf("expected quota in quotas cache")
		})
		if err != nil {
			// It's weird for a logged in user to not have a quota. Log it, be generous, and set a new quota.
			// It may be a timing issue depending on when the underlying expires.
			fmt.Printf("expected a quota for user %s, but found nothing (not even 0)\n", accountID)
			qs := strconv.Itoa(maxQuota)
			wa.cache.SetNx(fmt.Sprintf("quotas:%s", accountID), qs, maxQuotaLifespanSeconds)
			quota = qs
		}

		email, err := wa.cache.Get(fmt.Sprintf("accounts:%s", accountID), func() (string, error) {
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

func (wa *Webapp) withSufficientQuota(next http.HandlerFunc) http.HandlerFunc {
	return wa.withAccountDetails(func(w http.ResponseWriter, r *http.Request) {
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

// GetHome implements home route "GET /" for the web app, serving the home page
// and initializing the app.
func (wa *Webapp) GetHome(w http.ResponseWriter, r *http.Request) {
	var quota, email string
	if q, ok := r.Context().Value(quotaContextName).(string); ok {
		quota = q
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "quota not loaded in context")
		return
	}
	if e, ok := r.Context().Value(emailContextName).(string); ok {
		email = e
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "email not loaded in context")
		return
	}

	data := struct {
		Email string
		Quota string
	}{
		Email: email,
		Quota: quota,
	}

	t := wa.tmpl.Lookup("pages/home.html")
	if t == nil {
		// Set a 500 status on the response
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to lookup template: %s", "pages/home.html")
		return
	}

	if err := wa.tmpl.Lookup("pages/home.html").Execute(w, data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to render template: %v", err)
	}
}

// PostCreateRecipe implements a route at "POST /recipes/summary" to create a
// new analysis of a recipe indicated by the url field in the form submission.
// Returns an HTML partial with the summarized analysis of the given recipe.
func (wa *Webapp) PostCreateRecipe(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	fmt.Println("Handling PostCreateRecipe")
	u := r.PathValue("url")
	if u == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "URL required")
		return
	}

	raw, err := wa.cache.Get(fmt.Sprintf("recipes:raw:%s", u), func() (string, error) {
		recipeUrl, err := url.PathUnescape(u)
		if err != nil {
			return "", fmt.Errorf("invalid URL encoding (%s): %v", u, err)
		}
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
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to fetch raw: %v", err)
		return
	}

	md, err := wa.cache.Get(fmt.Sprintf("recipes:parsed:%s", u), func() (string, error) {
		return helpers.CreateMarkdownFromRaw(u, raw)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to get content from page: %v", err)
		return
	}

	// TODO Provide a way for the LLM to indicate it couldn't summarize the recipe
	model, err := models.MakeBedrockModel(ctx)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to initialize model: %v", err)
		return
	}

	summary, err := wa.cache.Get(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
		return models.SummarizeRecipe(ctx, model, md)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to summarize recipe contents: %v", err)
		return
	}

	tmp := struct {
		Summary string `json:"summary"`
	}{
		Summary: summary,
	}

	w.Header().Add("Content-Type", "application/json")
	out, err := json.Marshal(tmp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "{ \"error\": \"unable to render summary JSON: %v\"", err)
	}
	fmt.Fprint(w, string(out))
}

// GetRecipeWineSuggestions implements the route at
// "GET /recipes/suggestions/{url}". Note that "POST /recipes" MUST be called first.
// Otherwise, this route calls a bad request error since the recipe summary
// hasn't been cached yet. This introduces a stateful dependency, but it
// minimizes the need to pass the summary to this endpoint in the request.
func (wa *Webapp) GetRecipeWineSuggestions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	fmt.Println("Handling GetRecipeWineSuggestions")

	u := r.PathValue("url")
	if u == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "URL required")
		return
	}

	summary, err := wa.cache.Get(fmt.Sprintf("recipes:summarized:%s", u), func() (string, error) {
		return "", fmt.Errorf("expected a summary to be generated before this call")
	})
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "unable to load summary: %v", err)
		return
	}

	model, err := models.MakeBedrockModel(ctx)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to initialize model: %v", err)
		return
	}

	suggestions, err := wa.cache.Get(fmt.Sprintf("recipes:suggestions-json:%s", u), func() (string, error) {
		// Only decrement quota if the user has a cache miss
		accountID := r.Context().Value(sessionContextName)
		if a, ok := accountID.(string); ok {
			wa.cache.Decr(sessionQuotaKey(a))
		} else {
			return "", fmt.Errorf("unexpected session context type")
		}

		return models.GeneratePairingSuggestions(ctx, model, summary)
	})

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to get wine suggestions from the model: %v", err)
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
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to scan keys: %v", err)
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

	out, err := json.Marshal(urls[0:3])
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to encode URL suggestions: %v", err)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	fmt.Fprint(w, string(out))
}

func (wa *Webapp) GetLogin(w http.ResponseWriter, r *http.Request) {
	context := struct {
		GoogleClientID string
		Hostname       string
	}{
		GoogleClientID: wa.googleClientID,
		Hostname:       wa.hostname,
	}
	if err := wa.tmpl.Lookup("pages/login.html").Execute(w, context); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to render login page")
	}
}

func (wa *Webapp) DeleteSession(w http.ResponseWriter, r *http.Request) {
	accountID := r.Context().Value(sessionContextName)
	if err := wa.cache.Delete(fmt.Sprintf("sessions:%s", accountID)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to destroy session: %v", err)
	}

	wa.deleteCookie(sessionCookieName, w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (wa *Webapp) PostOauthResponse(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "unable to parse request form: %v", err)
		return
	}

	csrfToken := r.Form["g_csrf_token"][0]
	csrfCookie, err := wa.getCookie("g_csrf_token", r)

	if err == http.ErrNoCookie {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "no csrf cookie set")
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to get csrf cookie: %v", err)
		return
	}
	if csrfToken != csrfCookie.Value {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "failed to verify double submit cookie")
		return
	}

	expectedMethod := "RS256"
	secret, err := helpers.GetGoogleJWTToken(expectedMethod)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "unable to fetch latest Google certs: %v", err)
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
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to parse response as Google JWT")
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
