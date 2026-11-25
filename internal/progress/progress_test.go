package progress

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to write from the drawing goroutine and
// read from the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fast(t *testing.T) {
	t.Helper()
	d, r := delay, refresh
	delay, refresh = 0, time.Millisecond
	t.Cleanup(func() { delay, refresh = d, r })
}

func waitFor(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output never contained %q; got %q", want, buf.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBarDrawsAndErases(t *testing.T) {
	fast(t)
	var buf syncBuffer
	b := start(&buf, func() int { return 200 }, "Reading", 4)
	b.Add(2)
	waitFor(t, &buf, "2/4  50%")
	b.Done()
	b.Done() // idempotent
	out := buf.String()
	if !strings.HasSuffix(out, "\r\033[K") {
		t.Errorf("line not erased at the end: %q", out)
	}
	if !strings.Contains(out, "Reading  ████████████░░░░░░░░░░░░  2/4  50%") {
		t.Errorf("unexpected bar: %q", out)
	}
}

func TestSpinnerForUnknownTotal(t *testing.T) {
	fast(t)
	var buf syncBuffer
	b := start(&buf, func() int { return 200 }, "Listing secret/", 0)
	b.Add(1234)
	waitFor(t, &buf, "Listing secret/  1,234 found")
	b.Done()
}

func TestNothingDrawnWhenFast(t *testing.T) {
	var buf syncBuffer
	b := start(&buf, func() int { return 200 }, "Reading", 10) // default 250ms delay
	b.Add(10)
	b.Done()
	if buf.String() != "" {
		t.Errorf("a quick operation should draw nothing, got %q", buf.String())
	}
}

func TestTruncatesToTerminalWidth(t *testing.T) {
	fast(t)
	var buf syncBuffer
	b := start(&buf, func() int { return 11 }, "Reading a very long label", 10)
	waitFor(t, &buf, "\r\033[K")
	b.Done()
	for _, l := range strings.Split(buf.String(), "\r\033[K") {
		if n := len([]rune(l)); n > 10 {
			t.Errorf("line %q is %d runes, want at most 10", l, n)
		}
	}
}

func TestLine(t *testing.T) {
	b := &Bar{label: "Applying", total: 200}
	b.n.Store(50)
	if got, want := b.line(10*time.Second), "Applying  ██████░░░░░░░░░░░░░░░░░░  50/200  25%  ETA 30s"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	b.n.Store(500) // more than total: capped
	if got := b.line(time.Second); !strings.Contains(got, "200/200  100%") || strings.Contains(got, "ETA") {
		t.Errorf("line over total = %q", got)
	}
}

func TestRemaining(t *testing.T) {
	for _, tc := range []struct {
		elapsed  time.Duration
		n, total int64
		want     string
		ok       bool
	}{
		{500 * time.Millisecond, 1, 10, "", false}, // too early
		{time.Second, 0, 10, "", false},            // no rate yet
		{time.Second, 10, 10, "", false},           // finished
		{time.Second, 9, 10, "1s", true},           // rounds up to 1s
		{10 * time.Second, 1, 10, "1m30s", true},
		{time.Minute, 1, 100, "1h39m", true},
	} {
		got, ok := remaining(tc.elapsed, tc.n, tc.total)
		if got != tc.want || ok != tc.ok {
			t.Errorf("remaining(%v, %d, %d) = %q, %v; want %q, %v", tc.elapsed, tc.n, tc.total, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestNilIsANoOp(t *testing.T) {
	var f Factory
	b := f.Start("x", 1)
	if b != nil {
		t.Fatal("nil Factory should start a nil Bar")
	}
	b.Add(1)
	b.Done()
}

func TestForNeedsATerminal(t *testing.T) {
	if For(&bytes.Buffer{}, true) != nil {
		t.Error("a buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if For(f, true) != nil {
		t.Error("a regular file is not a terminal")
	}
	if For(os.Stderr, false) != nil {
		t.Error("disabled should give nil")
	}
}
