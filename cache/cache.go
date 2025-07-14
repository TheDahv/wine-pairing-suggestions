package cache

import (
	"context"
	"fmt"
	"strings"
	"time"

	rdb "github.com/redis/go-redis/v9"
)

// Resolver is a function that returns a cacheable resource on a cache miss.
type Resolver func() (string, error)

// Cacher describes the functionality of a cache provider.
type Cacher interface {
	Get(string, Resolver) (string, error)
	Set(string, string) error
	SetEx(string, string, int) error
	GetKeys(string) ([]string, error)
}

type memory struct {
	cache map[string]string
}

// NewMemory creates a new in-memory cache
func NewMemory() *memory {
	return &memory{cache: make(map[string]string)}
}

func (m *memory) Get(key string, onMiss Resolver) (string, error) {
	if hit, ok := m.cache[key]; ok {
		return hit, nil
	}

	val, err := onMiss()
	if err != nil {
		return "", fmt.Errorf("unable to resolve cache miss: %v", err)
	}

	m.cache[key] = val
	return m.cache[key], nil
}

func (m *memory) GetKeys(pattern string) ([]string, error) {
	var keys []string
	search := strings.Replace(pattern, "*", "", 1)
	for k := range m.cache {
		if strings.HasPrefix(k, search) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memory) Set(key string, val string) error {
	m.cache[key] = val
	return nil
}

func (m *memory) SetEx(key string, val string, seconds int) error {
	return m.Set(key, val)
}

type redis struct {
	conn *rdb.Client
}

func NewRedis(host string, port int) *redis {
	return &redis{conn: rdb.NewClient(&rdb.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port),
		Password: "",
		DB:       0,
	})}
}

func (r *redis) Get(key string, onMiss Resolver) (string, error) {
	ctx := context.TODO()
	hit, err := r.conn.Get(ctx, key).Result()

	if err != nil && err != rdb.Nil {
		return "", fmt.Errorf("unable to fetch from Redis: %v", err)
	}

	// Handle cache miss
	if err == rdb.Nil {
		val, err := onMiss()
		if err != nil {
			return "", fmt.Errorf("unable to resolve cache miss: %v", err)
		}

		if err := r.conn.Set(ctx, key, val, 0).Err(); err != nil {
			// Log and eat error. Not worth crashing the request.
			// TODO Replace with proper logger
			fmt.Printf("unable to cache resolved cache value: %v\n", err)
		}

		return val, nil
	}

	return hit, nil
}

func (r *redis) GetKeys(pattern string) ([]string, error) {
	ctx := context.TODO()
	keys, err := r.conn.Keys(ctx, pattern).Result()
	if err != nil {
		return keys, fmt.Errorf("unable to fetch keys from redis: %v", err)
	}

	return keys, err
}

func (r *redis) Set(key string, val string) error {
	return r.SetEx(key, val, 0)
}

func (r *redis) SetEx(key string, val string, seconds int) error {
	ctx := context.TODO()
	return r.conn.Set(ctx, key, val, time.Duration(seconds)*time.Second).Err()
}
