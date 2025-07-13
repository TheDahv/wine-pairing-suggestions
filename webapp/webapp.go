package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/thedahv/wine-pairing-suggestions/cache"
	"github.com/thedahv/wine-pairing-suggestions/helpers"
	"github.com/thedahv/wine-pairing-suggestions/models"
)

// Webapp contains handlers and functionality suited for implementing a wine suggestions web application.
// Construct a new Webapp with NewWebapp.
type Webapp struct {
	port  int
	tmpl  *template.Template
	cache cache.Cacher
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

// NewWebapp builds a new Webapp configured and ready to listen to traffic on
// the given port. Call Start on a new webapp to begin receiving traffic.
func NewWebapp(port int, options ...Option) (*Webapp, error) {
	wa := &Webapp{
		port: port,
		tmpl: template.New(""),
	}

	_, thispath, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("unable to determine current package location")
	}
	thisdir := filepath.Dir(thispath)

	if err := wa.buildTemplates(filepath.Join(thisdir, "templates")); err != nil {
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
func (wa *Webapp) Start(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /recipes/summary/{url}", wa.PostCreateRecipe)
	mux.HandleFunc("GET /recipes/suggestions/recent", wa.GetRecentSuggestions)
	mux.HandleFunc("GET /recipes/suggestions/{url}", wa.GetRecipeWineSuggestions)
	mux.HandleFunc("GET /", wa.GetHome)

	fmt.Printf("listening on :%d\n", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", wa.port), mux)
}

// buildTemplates finds, compiles, and registers all view templates for this
// webapp for use in route handlers, throwing an error if anything fails to
// compile. Templates are named by their file path (including extension) within
// the templates folder.  For example, a template at
// "webapp/templates/folder/template.html" will be called
// "folder/template.html". Use wa.tmpl.Lookup("folder/template.html") to use it
// in a handler.
func (wa *Webapp) buildTemplates(root string) error {
	return filepath.WalkDir(root, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error visiting template at %s: %v", path, err)
		}

		if info.IsDir() {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("unable to read template at %s: %v", path, err)
		}

		name := strings.Replace(path, root+string(filepath.Separator), "", 1)
		template.Must(wa.tmpl.New(name).Parse(string(contents)))
		return nil
	})
}

// GetHome implements home route "GET /" for the web app, serving the home page
// and initializing the app.
func (wa *Webapp) GetHome(w http.ResponseWriter, r *http.Request) {
	t := wa.tmpl.Lookup("pages/home.html")
	if t == nil {
		// Set a 500 status on the response
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to lookup template: %s", "pages/home.html")
		return
	}

	if err := wa.tmpl.Lookup("pages/home.html").Execute(w, nil); err != nil {
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

	//u := r.FormValue("url")
	u := r.PathValue("url")
	fmt.Println("url param")
	if u == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "URL required")
		return
	}

	recipeUrl, err := url.PathUnescape(u)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid URL encoding: %v", err)
		return
	}

	fmt.Println("loading url", recipeUrl)
	raw, err := wa.cache.Get(fmt.Sprintf("recipes:raw:%s", u), func() (string, error) {
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
		return helpers.CreateMarkdownFromRaw(recipeUrl, raw)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unable to get content from page: %v", err)
		return
	}

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
		RecipeURL string `json:"url"`
		Summary   string `json:"summary"`
	}{
		RecipeURL: recipeUrl,
		Summary:   summary,
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
	fmt.Println("url param")
	if u == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "URL required")
		return
	}

	recipeUrl, err := url.PathUnescape(u)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid URL encoding: %v", err)
		return
	}

	fmt.Println("loading url", recipeUrl)

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
		u := k[strings.Index(k, ":")+1:]
		u = u[strings.Index(u, ":")+1:]
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
	return
}
