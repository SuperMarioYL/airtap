// Package tui renders the streaming agent log to the terminal.
//
// The thin-client daemon pipes agent-loop output (which arrives over the mTLS
// connection from the box) through Stream.Render, one line at a time, so the
// developer sees tool calls + diffs live — the "cloud runner feel" from MVP plan
// §1, but with 数据不出境 honored.
package tui

import (
	"bufio"
	"fmt"
	"os"
	"sync"
)

// Stream is a simple line-by-line terminal writer.
type Stream struct {
	mu sync.Mutex
	w  *bufio.Writer
}

// NewStream returns a Stream writing to os.Stdout.
func NewStream() *Stream {
	return &Stream{w: bufio.NewWriter(os.Stdout)}
}

// Render writes a single line to the terminal and flushes immediately so
// streaming output appears live. A trailing newline is appended if line lacks
// one.
func (s *Stream) Render(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.WriteString(line); err != nil {
		return fmt.Errorf("tui: write: %w", err)
	}
	if !endsWithNewline(line) {
		if _, err := s.w.WriteString("\n"); err != nil {
			return fmt.Errorf("tui: write newline: %w", err)
		}
	}
	return s.w.Flush()
}

// Close flushes any buffered output.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Flush()
}

func endsWithNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}
