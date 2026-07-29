package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/markgustetic/sentra/internal/ui"
)

const setupSpinnerTickInterval = 120 * time.Millisecond

var setupSpinnerFrames = []string{"-", "\\", "|", "/"}

var setupCanAnimate = setupWriterIsTerminal

type setupProgress struct {
	out      io.Writer
	label    string
	animated bool
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func runSetupProgress(out io.Writer, progressLabel string, successLabel string, fn func() error) error {
	step := startSetupProgress(out, progressLabel)
	err := fn()
	if err != nil {
		step.Fail()
		return err
	}
	step.Success(successLabel)
	return nil
}

func startSetupProgress(out io.Writer, label string) *setupProgress {
	step := &setupProgress{out: out, label: label}
	if !setupCanAnimate(out) {
		printSetupStep(out, label)
		return step
	}

	step.animated = true
	step.done = make(chan struct{})
	step.wg.Add(1)
	fmt.Fprintf(out, "\r%s %s", ui.Subtle.Render(setupSpinnerFrames[0]), label)
	go func() {
		defer step.wg.Done()
		ticker := time.NewTicker(setupSpinnerTickInterval)
		defer ticker.Stop()
		frame := 1
		for {
			select {
			case <-step.done:
				return
			case <-ticker.C:
				fmt.Fprintf(out, "\r%s %s", ui.Subtle.Render(setupSpinnerFrames[frame%len(setupSpinnerFrames)]), label)
				frame++
			}
		}
	}()
	return step
}

func (s *setupProgress) Success(label string) {
	if !s.animated {
		printSetupOK(s.out, label)
		return
	}
	s.stop()
	fmt.Fprintf(s.out, "\r\033[2K%s %s\n", ui.Success.Render("ok"), label)
}

func (s *setupProgress) Fail() {
	if !s.animated {
		return
	}
	s.stop()
	fmt.Fprintf(s.out, "\r\033[2K%s %s\n", ui.Danger.Render("error"), s.label)
}

func (s *setupProgress) stop() {
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
	})
}

func setupWriterIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}
