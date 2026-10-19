package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonklasocute/go-ratelimit/algorithm"
)

var titles = map[algorithm.ID]string{
	algorithm.FixedWindow:   "Fixed window counter",
	algorithm.SlidingLog:    "Sliding window log",
	algorithm.SlidingWindow: "Sliding window counter",
	algorithm.TokenBucket:   "Token bucket",
	algorithm.LeakyBucket:   "Leaky bucket (meter)",
	algorithm.GCRA:          "GCRA",
}

// Geometry. Both panels share the x scale; their y scales are fixed so the
// charts of different algorithms can be compared side by side.
const (
	width, height = 760, 420
	left, right   = 52, 16
	plotW         = width - left - right

	barTop, barBottom   = 104, 236 // panel 1: requests per bin
	barMax              = 30
	lineTop, lineBottom = 284, 376 // panel 2: allowed in the trailing second
	lineMax             = 25
	axisY               = 396
)

// The palette is the dataviz reference instance (validated for CVD in both
// modes); text never takes a series color.
const style = `
svg{--surface:#fcfcfb;--text:#0b0b0b;--muted:#52514e;--grid:#e6e5e0;--allowed:#2a78d6;--denied:#eb6834;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
@media (prefers-color-scheme:dark){svg{--surface:#1a1a19;--text:#f0efec;--muted:#c3c2b7;--grid:#363531;--allowed:#3987e5;--denied:#d95926}}
.bg{fill:var(--surface)}.t{fill:var(--text)}.m{fill:var(--muted)}
.grid{stroke:var(--grid);stroke-width:1}.edge{stroke:var(--grid);stroke-width:1;stroke-dasharray:2 3}
.a{fill:var(--allowed)}.d{fill:var(--denied)}
.line{fill:none;stroke:var(--allowed);stroke-width:2;stroke-linejoin:round}
.limit{stroke:var(--muted);stroke-width:1;stroke-dasharray:5 4}
text{font-size:11px}.title{font-size:15px;font-weight:600}.sub{font-size:12px}`

func xOf(d time.Duration) float64 { return left + float64(d)/float64(duration)*plotW }

func yBar(v float64) float64 { return barBottom - v/barMax*(barBottom-barTop) }

func yLine(v float64) float64 { return lineBottom - v/lineMax*(lineBottom-lineTop) }

// render draws one algorithm's chart.
func render(id algorithm.ID, reqs []time.Duration, allowed []bool, s stats) string {
	nbins := int(duration / bin)
	ok := make([]int, nbins)
	denied := make([]int, nbins)
	for i, at := range reqs {
		b := min(int(at/bin), nbins-1)
		if allowed[i] {
			ok[b]++
		} else {
			denied[b]++
		}
	}

	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	title := titles[id]
	desc := fmt.Sprintf("%s, limit 10 requests per second with burst 10: allowed %d of %d requests; at most %d allowed in any one-second interval.",
		title, s.allowed, s.offered, s.maxPerSecond)

	p(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	p("\n<title id=\"title\">%s</title>\n<desc id=\"desc\">%s</desc>\n<style>%s\n</style>\n", title, desc, style)
	p(`<rect class="bg" width="%d" height="%d" rx="8"/>`+"\n", width, height)

	// Header: title, headline numbers, legend.
	p(`<text class="t title" x="%d" y="26">%s</text>`+"\n", left, title)
	p(`<text class="m sub" x="%d" y="46">Allowed %d of %d requests · at most %d in any 1 s (limit 10/s, burst 10)</text>`+"\n",
		left, s.allowed, s.offered, s.maxPerSecond)
	legend(&b, width-right-170, 22)

	// Phase labels above panel 1.
	for _, ph := range phases {
		p(`<text class="m" x="%.1f" y="%d" text-anchor="middle">%s</text>`+"\n", (xOf(ph.start)+xOf(ph.end))/2, barTop-12, ph.label)
	}

	// Window edges at whole seconds, through both panels.
	for s := time.Duration(0); s <= duration; s += time.Second {
		x := xOf(s)
		p(`<line class="edge" x1="%.1f" y1="%d" x2="%.1f" y2="%d"/>`+"\n", x, barTop, x, lineBottom)
		p(`<text class="m" x="%.1f" y="%d" text-anchor="middle">%ds</text>`+"\n", x, axisY+4, int(s/time.Second))
	}

	// Panel 1: stacked bars per 100 ms, allowed at the baseline.
	p(`<text class="m" x="%d" y="%d">requests per 100 ms</text>`+"\n", left, barTop-28)
	for _, v := range []float64{0, 10, 20, 30} {
		y := yBar(v)
		p(`<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`+"\n", left, y, width-right, y)
		p(`<text class="m" x="%d" y="%.1f" text-anchor="end">%g</text>`+"\n", left-6, y+4, v)
	}
	binW := float64(plotW) / float64(nbins)
	for i := 0; i < nbins; i++ {
		if ok[i]+denied[i] == 0 {
			continue
		}
		x := left + float64(i)*binW + 1
		w := binW - 2
		p(`<g><title>%.1f–%.1fs: %d allowed, %d denied</title>`, float64(i)/10, float64(i+1)/10, ok[i], denied[i])
		top := float64(barBottom)
		if ok[i] > 0 {
			h := float64(barBottom) - yBar(float64(ok[i]))
			p(`<path class="a" d="%s"/>`, barPath(x, top-h, w, h, denied[i] == 0))
			top -= h + 2 // surface gap between stacked segments
		}
		if denied[i] > 0 {
			h := max(float64(barBottom)-yBar(float64(denied[i]))-2, 1)
			p(`<path class="d" d="%s"/>`, barPath(x, top-h, w, h, true))
		}
		p("</g>\n")
	}

	// Panel 2: allowed requests in the trailing second, against the limit.
	p(`<text class="m" x="%d" y="%d">allowed in the last 1 s</text>`+"\n", left, lineTop-12)
	for _, v := range []float64{0, 10, 20} {
		y := yLine(v)
		p(`<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`+"\n", left, y, width-right, y)
		p(`<text class="m" x="%d" y="%.1f" text-anchor="end">%g</text>`+"\n", left-6, y+4, v)
	}
	p(`<line class="limit" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`+"\n", left, yLine(10), width-right, yLine(10))
	p(`<text class="m" x="%d" y="%.1f">limit</text>`+"\n", left+4, yLine(10)-5)
	p(`<path class="line" d="%s"/>`+"\n", trailingPath(reqs, allowed))
	if peak, at := peakTrailing(reqs, allowed); peak > 0 {
		p(`<text class="t" x="%.1f" y="%.1f" text-anchor="middle">peak %d</text>`+"\n", xOf(at), yLine(float64(peak))-8, peak)
	}

	b.WriteString("</svg>\n")
	return b.String()
}

func legend(b *strings.Builder, x, y int) {
	fmt.Fprintf(b, `<rect class="a" x="%d" y="%d" width="10" height="10" rx="2"/><text class="t" x="%d" y="%d">allowed</text>`+"\n", x, y-9, x+15, y)
	fmt.Fprintf(b, `<rect class="d" x="%d" y="%d" width="10" height="10" rx="2"/><text class="t" x="%d" y="%d">denied</text>`+"\n", x+85, y-9, x+100, y)
}

// barPath is a rectangle whose top corners are rounded when it ends the bar.
func barPath(x, y, w, h float64, roundTop bool) string {
	r := 0.0
	if roundTop {
		r = min(4, w/2, h)
	}
	return fmt.Sprintf("M%.1f %.1fV%.1fQ%.1f %.1f %.1f %.1fH%.1fQ%.1f %.1f %.1f %.1fV%.1fZ",
		x, y+h, y+r, x, y, x+r, y, x+w-r, x+w, y, x+w, y+r, y+h)
}

// trailing returns the number of allowed requests in (at-1s, at].
func trailing(reqs []time.Duration, allowed []bool, at time.Duration) int {
	n := 0
	for i, r := range reqs {
		if allowed[i] && r > at-time.Second && r <= at {
			n++
		}
	}
	return n
}

// trailingPath samples the trailing count every 10 ms as a step line.
func trailingPath(reqs []time.Duration, allowed []bool) string {
	var b strings.Builder
	prev := -1
	for at := time.Duration(0); at <= duration; at += 10 * time.Millisecond {
		n := trailing(reqs, allowed, at)
		x, y := xOf(at), yLine(float64(n))
		switch {
		case prev < 0:
			fmt.Fprintf(&b, "M%.1f %.1f", x, y)
		case n != prev:
			fmt.Fprintf(&b, "H%.1fV%.1f", x, y)
		}
		prev = n
	}
	fmt.Fprintf(&b, "H%.1f", xOf(duration))
	return b.String()
}

// peakTrailing returns the highest trailing count and when it first occurs.
func peakTrailing(reqs []time.Duration, allowed []bool) (int, time.Duration) {
	peak, when := 0, time.Duration(0)
	for at := time.Duration(0); at <= duration; at += 10 * time.Millisecond {
		if n := trailing(reqs, allowed, at); n > peak {
			peak, when = n, at
		}
	}
	return peak, when
}
