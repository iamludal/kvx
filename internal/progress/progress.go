// Package progress draws a single, self-erasing progress line on a terminal
// while kvx walks, reads or changes many secrets.
//
// It writes only to stderr-like writers, only when they are a terminal, and
// only once an operation has lasted long enough to be worth showing, so short
// commands, pipes and CI logs look exactly the same as without it.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// Timings, variables so that tests can shorten them.
var (
	delay   = 250 * time.Millisecond // nothing is drawn before this
	refresh = 100 * time.Millisecond
)

// Factory starts a bar. A nil Factory starts nothing: every command can call
// it unconditionally.
type Factory func(label string, total int) *Bar

// Start starts a bar, or returns nil (a valid, no-op *Bar) if f is nil.
func (f Factory) Start(label string, total int) *Bar {
	if f == nil {
		return nil
	}
	return f(label, total)
}

// For returns a Factory drawing on w, or nil when progress should not be
// shown: disabled, w is not a terminal, or TERM=dumb.
func For(w io.Writer, enabled bool) Factory {
	f, ok := w.(*os.File)
	if !enabled || !ok || !term.IsTerminal(int(f.Fd())) || os.Getenv("TERM") == "dumb" {
		return nil
	}
	width := func() int {
		if c, _, err := term.GetSize(int(f.Fd())); err == nil && c > 0 {
			return c
		}
		return 80
	}
	return func(label string, total int) *Bar { return start(w, width, label, total) }
}

// Bar is one progress line. Its methods are safe for concurrent use, and a nil
// *Bar does nothing.
type Bar struct {
	w     io.Writer
	width func() int
	label string
	total int64
	n     atomic.Int64
	begin time.Time

	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	drawn bool // only touched by the drawing goroutine, read after done
	frame int
}

func start(w io.Writer, width func() int, label string, total int) *Bar {
	b := &Bar{
		w: w, width: width, label: label, total: int64(total), begin: time.Now(),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go b.loop()
	return b
}

// Add records n more items done.
func (b *Bar) Add(n int) {
	if b != nil {
		b.n.Add(int64(n))
	}
}

// Done erases the line. Call it before printing anything else on the same
// writer. Calling it more than once is fine.
func (b *Bar) Done() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		close(b.stop)
		<-b.done
		if b.drawn {
			fmt.Fprint(b.w, "\r\033[K")
		}
	})
}

func (b *Bar) loop() {
	defer close(b.done)
	select {
	case <-b.stop:
		return
	case <-time.After(delay):
	}
	t := time.NewTicker(refresh)
	defer t.Stop()
	for {
		b.draw()
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
	}
}

func (b *Bar) draw() {
	line := b.line(time.Since(b.begin))
	if w := b.width() - 1; len([]rune(line)) > w && w > 0 {
		line = string([]rune(line)[:w])
	}
	fmt.Fprint(b.w, "\r\033[K"+line)
	b.drawn = true
	b.frame++
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const barWidth = 24

// line renders the bar after elapsed time.
func (b *Bar) line(elapsed time.Duration) string {
	n := b.n.Load()
	if b.total <= 0 {
		// Unknown total: a spinner and a running count.
		return fmt.Sprintf("%c %s  %s found  %s", spinner[b.frame%len(spinner)], b.label, count(n), seconds(elapsed))
	}
	n = min(n, b.total)
	filled := int(int64(barWidth) * n / b.total)
	s := fmt.Sprintf("%s  %s%s  %s/%s  %d%%", b.label,
		strings.Repeat("█", filled), strings.Repeat("░", barWidth-filled),
		count(n), count(b.total), 100*n/b.total)
	if eta, ok := remaining(elapsed, n, b.total); ok {
		s += "  ETA " + eta
	}
	return s
}

// remaining estimates the time left from the rate so far, once there is
// enough to go on.
func remaining(elapsed time.Duration, n, total int64) (string, bool) {
	if n == 0 || n >= total || elapsed < time.Second {
		return "", false
	}
	left := time.Duration(float64(elapsed) / float64(n) * float64(total-n))
	return short(left), true
}

func short(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 1))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// count formats n with thousands separators: 12,345.
func count(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
