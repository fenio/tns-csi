package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"
)

// printStep prints a step-style line (icon + message) without a spinner.
func printStep(icon, msg string) {
	fmt.Printf("%s %s\n", icon, msg)
}

// printStepf prints a formatted step-style line.
func printStepf(c *color.Color, icon, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s\n", c.Sprint(icon), msg)
}

// spinner shows an animated spinner on stderr while a long operation runs.
// Call stop() when done.
//
//nolint:govet // field alignment not critical for CLI utility struct
type spinner struct {
	msg  string
	done chan struct{}
	once sync.Once
}

// newSpinner starts a spinner with the given message. The spinner renders
// to stderr so it doesn't interfere with stdout output.
func newSpinner(msg string) *spinner {
	s := &spinner{
		msg:  msg,
		done: make(chan struct{}),
	}

	if !isTerminal() {
		// Not a TTY — just print a static line
		fmt.Fprintf(os.Stderr, "%s %s\n", colorMuted.Sprint("..."), msg)
		return s
	}

	go func() {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				// Clear the spinner line
				fmt.Fprintf(os.Stderr, "\r\033[K")
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r%s %s", colorMuted.Sprint(frames[i%len(frames)]), msg)
				i++
			}
		}
	}()

	return s
}

// stop stops the spinner animation.
func (s *spinner) stop() {
	s.once.Do(func() { close(s.done) })
}

// isTerminal checks if stderr is a terminal (for spinner rendering).
func isTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
