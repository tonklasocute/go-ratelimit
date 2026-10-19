package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tonklasocute/go-ratelimit/algorithm"
)

func TestSimulation(t *testing.T) {
	reqs := traffic()
	want := map[algorithm.ID]int{ // max allowed in any 1s interval
		algorithm.FixedWindow: 20, // the double burst at the window edge
		algorithm.SlidingLog:  10, // exact
	}
	for _, id := range algorithm.All {
		s := summarize(reqs, simulate(id, reqs))
		if s.offered != len(reqs) || s.allowed == 0 || s.allowed >= s.offered {
			t.Errorf("%v: %+v", id, s)
		}
		// Buckets allow burst + rate×1s; nothing allows more.
		if s.maxPerSecond > 20 {
			t.Errorf("%v: %d in one second", id, s.maxPerSecond)
		}
		if w, ok := want[id]; ok && s.maxPerSecond != w {
			t.Errorf("%v: max per second %d, want %d", id, s.maxPerSecond, w)
		}
	}
}

func TestRenderIsWellFormedSVG(t *testing.T) {
	reqs := traffic()
	for _, id := range algorithm.All {
		allowed := simulate(id, reqs)
		svg := render(id, reqs, allowed, summarize(reqs, allowed))
		dec := xml.NewDecoder(strings.NewReader(svg))
		for {
			_, err := dec.Token()
			if err != nil {
				if err.Error() != "EOF" {
					t.Fatalf("%v: invalid XML: %v", id, err)
				}
				break
			}
		}
		for _, want := range []string{"<title id=\"title\">" + titles[id], "prefers-color-scheme:dark", "allowed in the last 1 s"} {
			if !strings.Contains(svg, want) {
				t.Errorf("%v: missing %q", id, want)
			}
		}
	}
}

func TestMainWritesFiles(t *testing.T) {
	dir := t.TempDir()
	os.Args = []string{"visualize", "-out", dir}
	stdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	main()
	os.Stdout = stdout
	for _, id := range algorithm.All {
		if _, err := os.Stat(filepath.Join(dir, id.String()+".svg")); err != nil {
			t.Error(err)
		}
	}
}
