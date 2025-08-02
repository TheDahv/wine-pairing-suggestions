package lambdahelpers

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// pathValueKey is used as the key for storing path values in context
type pathValueKey string

// PathValueAdapter provides PathValue functionality for requests
type PathValueAdapter struct {
	*http.Request
}

// PathValue retrieves path values from the request context (for Lambda compatibility)
func (r *PathValueAdapter) PathValue(key string) string {
	if value := r.Context().Value(pathValueKey("pathvalue:" + key)); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// WithPathValue creates a new request with a path value set in context
func WithPathValue(r *http.Request, key, value string) *http.Request {
	log.Println("setting WithPathValue for key: ", key)
	if key == "url" {
		log.Println("working with a url")
		if match, _ := regexp.MatchString("https:/[^/]", value); match {
			value = strings.Replace(value, "https:/", "https://", 1)
			log.Println("fixed url: ", value)
		}
	}

	ctx := context.WithValue(r.Context(), pathValueKey("pathvalue:"+key), value)
	return r.WithContext(ctx)
}

// GetPathValue retrieves a path value from request context
func GetPathValue(r *http.Request, key string) string {
	log.Println("getting WithPathValue for key: ", key)
	log.Println(r.Context())
	if value := r.Context().Value(pathValueKey("pathvalue:" + key)); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}
