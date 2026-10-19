// Command distributed is one instance of an API that shares its rate limit
// with every other instance through Redis. See docker-compose.yml.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	rlhttp "github.com/tonklasocute/go-ratelimit/middleware/http"
	redisstore "github.com/tonklasocute/go-ratelimit/store/redis"
)

func main() {
	instance := getenv("INSTANCE", "app")
	rdb := redis.NewClient(&redis.Options{Addr: getenv("REDIS_ADDR", "localhost:6379")})
	defer rdb.Close()

	// 20 requests per second per API key, across all instances.
	lim, err := ratelimit.New(ratelimit.PerSecond(20),
		ratelimit.WithStore(redisstore.New(rdb)),
		ratelimit.WithFailurePolicy(ratelimit.FailClosed),
		ratelimit.WithOnError(func(_ context.Context, key string, err error) {
			log.Printf("rate limiter error for %q: %v", key, err)
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /work", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Instance", instance)
		w.Write([]byte("done by " + instance + "\n"))
	})
	handler := rlhttp.New(lim, rlhttp.WithKeyFunc(rlhttp.KeyByHeader("X-API-Key")))(mux)

	srv := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("%s listening on :8080", instance)
	log.Fatal(srv.ListenAndServe())
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
