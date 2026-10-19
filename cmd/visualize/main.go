// Command visualize replays one bursty traffic pattern against every
// algorithm and draws an SVG chart per algorithm into docs/img/.
//
//	go run ./cmd/visualize            # writes docs/img/<algorithm>.svg
//	go run ./cmd/visualize -out /tmp  # elsewhere
//
// The traffic has a steady phase, a burst straddling a window edge, sustained
// overload, an idle gap, a spike and mild overload, so each chart shows how
// the algorithm treats edges, bursts and steady state. The SVG is written by
// hand with the standard library only. A Markdown summary table is printed
// to stdout.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/tonklasocute/go-ratelimit/algorithm"
)

// The simulated rule: 10 requests per second, burst 10.
var params = algorithm.Params{Limit: 10, Period: time.Second, Burst: 10}

const (
	duration = 8 * time.Second
	bin      = 100 * time.Millisecond
)

// t0 is a whole second, so window edges fall on whole seconds of the chart.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()

// phase is a labeled stretch of the traffic pattern.
type phase struct {
	label      string
	start, end time.Duration
}

var phases = []phase{
	{"steady 6/s", 0, 2 * time.Second},
	{"edge burst", 2800 * time.Millisecond, 3200 * time.Millisecond},
	{"overload 25/s", 3600 * time.Millisecond, 5600 * time.Millisecond},
	{"spike ×30", 6500 * time.Millisecond, 6600 * time.Millisecond},
	{"14/s", 6800 * time.Millisecond, 8 * time.Second},
}

// traffic returns request offsets from t0, in order.
func traffic() []time.Duration {
	var out []time.Duration
	every := func(start, end, gap time.Duration) {
		for t := start; t < end; t += gap {
			out = append(out, t)
		}
	}
	every(0, 2*time.Second, time.Second/6)                                   // steady, under the limit
	every(2800*time.Millisecond, 3000*time.Millisecond, 13*time.Millisecond) // 15 just before the 3s edge
	every(3000*time.Millisecond, 3200*time.Millisecond, 13*time.Millisecond) // 15 just after it
	every(3600*time.Millisecond, 5600*time.Millisecond, 40*time.Millisecond) // sustained overload
	every(6500*time.Millisecond, 6530*time.Millisecond, time.Millisecond)    // spike of 30
	every(6800*time.Millisecond, 8000*time.Millisecond, time.Second/14)      // mild overload
	return out
}

// simulate returns, for each request, whether id allowed it.
func simulate(id algorithm.ID, reqs []time.Duration) []bool {
	var st algorithm.State
	allowed := make([]bool, len(reqs))
	for i, at := range reqs {
		allowed[i] = algorithm.Take(id, &st, params, t0+int64(at), 1).Allowed
	}
	return allowed
}

// stats summarizes a run.
type stats struct {
	offered, allowed int
	maxPerSecond     int // most allowed requests in any 1s interval
}

func summarize(reqs []time.Duration, allowed []bool) stats {
	var s stats
	var times []time.Duration
	for i, at := range reqs {
		s.offered++
		if allowed[i] {
			s.allowed++
			times = append(times, at)
		}
	}
	lo := 0
	for hi, at := range times {
		for times[lo] <= at-time.Second {
			lo++
		}
		s.maxPerSecond = max(s.maxPerSecond, hi-lo+1)
	}
	return s
}

func main() {
	out := flag.String("out", filepath.Join("docs", "img"), "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	reqs := traffic()
	fmt.Println("| Algorithm | Allowed / offered | Max allowed in any 1 s (limit 10) |")
	fmt.Println("|---|---|---|")
	for _, id := range algorithm.All {
		allowed := simulate(id, reqs)
		s := summarize(reqs, allowed)
		path := filepath.Join(*out, id.String()+".svg")
		if err := os.WriteFile(path, []byte(render(id, reqs, allowed, s)), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("| %s | %d / %d | %d |\n", titles[id], s.allowed, s.offered, s.maxPerSecond)
	}
}
