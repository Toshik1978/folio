package notify

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// windowSamples is how many entries an error summary lists.
const windowSamples = 5

// errorWindow groups Error-level records and run error lines into one summary
// per ErrorWindow.
type errorWindow struct {
	open     bool
	deadline time.Time // first entry time + ErrorWindow
	count    int       // all entries, incl. run lines
	samples  []string  // first windowSamples lines: "HH:MM:SS <message>: <error>" or a run line
}

// LogError enters an Error-level record that was not already reported into
// the error window. Only the record's message and its "error" attr are used.
// It returns at once when notifications are off or not ready and never blocks.
func (s *Service) LogError(_ context.Context, r slog.Record) {
	if !s.on() {
		return
	}
	s.addError(logLine(r))
}

// logLine renders r as "HH:MM:SS <message>[: <error>]", collapsed to one line
// of at most maxItemChars runes. The error text is used as is, file paths
// included. The time is formatted exactly as the log output formats it: slog
// stamps records with time.Now, which is in the server's local zone.
func logLine(r slog.Record) string {
	line := r.Time.Format(time.TimeOnly) + " " + r.Message
	if errText, ok := errorAttr(r); ok {
		line += ": " + errText
	}

	return item(line)
}

// errorAttr returns the text of r's last attr named "error". The attrs baked
// in through WithAttrs arrive on r ahead of the call-site attrs, so a
// call-site "error" wins.
func errorAttr(r slog.Record) (string, bool) {
	var (
		text  string
		found bool
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "error" {
			return true
		}
		v := a.Value.Resolve()
		text = v.String()
		if v.Kind() == slog.KindAny {
			if err, ok := v.Any().(error); ok {
				text = err.Error()
			}
		}
		found = true

		return true
	})

	return text, found
}

// addRunErrors enters the error lines of a completed run: books left for
// retry, and a post-sync fill that failed (FR-010).
func (s *Service) addRunErrors(o RunOutcome) {
	if o.PerBookErrors > 0 {
		s.addError(item("Library \"" + o.Library + "\": " + o.Kind.String() + " completed with " +
			strconv.Itoa(o.PerBookErrors) + " book(s) left for retry"))
	}
	if o.FillFailed != "" {
		s.addError(item("Library \"" + o.Library +
			"\": sync completed, but filling descriptions for new books failed: " + o.FillFailed))
	}
}

// addError enters one line, opening the window when it is closed.
func (s *Service) addError(line string) {
	now := s.now()
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	w := &s.window
	if !w.open {
		*w = errorWindow{open: true, deadline: now.Add(s.timing.ErrorWindow)}
	}
	w.count++
	if len(w.samples) < windowSamples {
		w.samples = append(w.samples, line)
	}
}

// tickErrors closes the window once its deadline has passed at now, sending
// its summary.
func (s *Service) tickErrors(now time.Time) {
	if w, ok := s.takeWindow(func(w *errorWindow) bool { return !now.Before(w.deadline) }); ok {
		s.enqueue(message{kind: KindErrorSummary, text: errorSummaryText(s.prefix, &w)})
	}
}

// flushErrors closes an open window into the queue whatever its deadline.
func (s *Service) flushErrors() {
	if w, ok := s.takeWindow(func(*errorWindow) bool { return true }); ok {
		s.enqueue(message{kind: KindErrorSummary, text: errorSummaryText(s.prefix, &w)})
	}
}

// takeWindow closes the window and returns its contents when it is open and
// due says so.
func (s *Service) takeWindow(due func(*errorWindow) bool) (errorWindow, bool) {
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	if !s.window.open || !due(&s.window) {
		return errorWindow{}, false
	}
	w := s.window
	s.window = errorWindow{}

	return w, true
}

// resetErrors discards the window.
func (s *Service) resetErrors() {
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	s.window = errorWindow{}
}

// errorSummaryText is the KindErrorSummary message.
func errorSummaryText(prefix string, w *errorWindow) string {
	var b strings.Builder
	b.WriteString(prefix + "\n" + strconv.Itoa(w.count) + " error(s) in the last 5 minutes")
	for _, l := range w.samples {
		b.WriteString("\n- " + l)
	}
	if more := w.count - windowSamples; more > 0 {
		b.WriteString("\n…and " + strconv.Itoa(more) + " more. See the server log for details.")
	}

	return truncate(b.String())
}
