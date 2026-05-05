package lsp

import "testing"

func TestStderrTail(t *testing.T) {
	t.Run("partial fill returns lines in order", func(t *testing.T) {
		tail := newStderrTail(3)
		tail.add("a")
		tail.add("b")
		if got, want := tail.snapshot(), "a\nb"; got != want {
			t.Errorf("snapshot=%q want %q", got, want)
		}
	})

	t.Run("empty buffer", func(t *testing.T) {
		tail := newStderrTail(3)
		if got := tail.snapshot(); got != "" {
			t.Errorf("snapshot=%q want empty", got)
		}
	})

	t.Run("wraparound keeps most recent N lines in chronological order", func(t *testing.T) {
		tail := newStderrTail(3)
		for _, line := range []string{"a", "b", "c", "d", "e"} {
			tail.add(line)
		}
		if got, want := tail.snapshot(), "c\nd\ne"; got != want {
			t.Errorf("snapshot=%q want %q", got, want)
		}
	})

	t.Run("exact fill at capacity", func(t *testing.T) {
		tail := newStderrTail(3)
		for _, line := range []string{"a", "b", "c"} {
			tail.add(line)
		}
		if got, want := tail.snapshot(), "a\nb\nc"; got != want {
			t.Errorf("snapshot=%q want %q", got, want)
		}
	})
}
