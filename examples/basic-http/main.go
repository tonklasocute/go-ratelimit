// Command basic-http rate limits a net/http server per client IP.
//
//	go run ./basic-http
//	for i in $(seq 7); do curl -si localhost:8080/hello | head -1; done
package main

import (
	"log"
	"net/http"
	"time"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	rlhttp "github.com/tonklasocute/go-ratelimit/middleware/http"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

func main() {
	store := memory.New()
	defer store.Close()

	// 5 requests per second per IP, bursts of up to 5.
	lim, err := ratelimit.New(ratelimit.PerSecond(5), ratelimit.WithStore(store))
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello\n"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	handler := rlhttp.New(lim,
		rlhttp.WithJSONBody(),
		rlhttp.WithSkip(func(r *http.Request) bool { return r.URL.Path == "/healthz" }),
	)(mux)

	srv := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Println("listening on :8080")
	log.Fatal(srv.ListenAndServe())
}
