package cache

import (
	"context"
	"fmt"

	rdb "github.com/redis/go-redis/v9"
)

type Resolver func() (string, error)

type Cacher interface {
	Get(string, Resolver) (string, error)
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
