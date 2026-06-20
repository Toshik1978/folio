package logging

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestLogging(t *testing.T) {
	suite.Run(t, new(loggingSuite))
}

type loggingSuite struct {
	suite.Suite
}

// logger builds a logger writing into a buffer the test can inspect. Asserting
// against the buffer keeps the tests on observable behavior (level, message,
// key=value attrs, env prefix) rather than the exact ANSI escape codes.
func (s *loggingSuite) logger(env string) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(newCustomHandler(&buf, false, env)), &buf
}

func (s *loggingSuite) TestRendersMessageAndAttrs() {
	log, buf := s.logger("")
	log.Info("hello", "count", 3, "name", "folio")

	out := buf.String()
	s.Contains(out, "INFO")
	s.Contains(out, "hello")
	s.Contains(out, "count=3")
	s.Contains(out, "name=folio")
}

// TestDebugSuppressedByDefaultLevel: the default threshold is Info, so debug
// records are dropped entirely.
func (s *loggingSuite) TestDebugSuppressedByDefaultLevel() {
	log, buf := s.logger("")
	log.Debug("noise")
	s.Empty(buf.String())
}

// TestDevEnablesDebug: the dev environment lowers the threshold to Debug.
func (s *loggingSuite) TestDevEnablesDebug() {
	log, buf := s.logger("dev")
	log.Debug("trace me")
	s.Contains(buf.String(), "trace me")
}

func (s *loggingSuite) TestEnvPrefix() {
	cases := map[string]string{
		"development": "[DEV]",
		"dev":         "[DEV]",
		"production":  "[PROD]",
		"prod":        "[PROD]",
		"staging":     "[staging]",
	}
	for env, want := range cases {
		log, buf := s.logger(env)
		log.Info("x")
		s.Contains(buf.String(), want, "env %q should tag the line with %q", env, want)
	}
}

func (s *loggingSuite) TestNoEnvHasNoPrefix() {
	log, buf := s.logger("")
	log.Info("plain")

	out := buf.String()
	s.NotContains(out, "[DEV]")
	s.NotContains(out, "[PROD]")
}

func (s *loggingSuite) TestWithAttrsRendersPersistentFields() {
	log, buf := s.logger("")
	log.With("svc", "books").Info("ping", "id", 7)

	out := buf.String()
	s.Contains(out, "svc=books", "attrs from With are carried onto every line")
	s.Contains(out, "id=7", "per-call attrs still render alongside persistent ones")
}

// TestWithAttrsPreservesLevelAndPrefix is a regression guard: deriving a logger
// with .With(...) must keep the configured level threshold and env prefix.
func (s *loggingSuite) TestWithAttrsPreservesLevelAndPrefix() {
	log, buf := s.logger("development")
	log.With("req", "42").Debug("scoped debug")

	out := buf.String()
	s.Contains(out, "scoped debug", "dev keeps debug enabled after .With")
	s.Contains(out, "[DEV]", "env prefix survives .With")
	s.Contains(out, "req=42")
}

// TestRendersUnknownLevel exercises the level-color fallback: a level outside
// Debug/Info/Warn/Error still renders rather than being dropped.
func (s *loggingSuite) TestRendersUnknownLevel() {
	log, buf := s.logger("")
	log.Log(s.T().Context(), slog.Level(12), "weird")
	s.Contains(buf.String(), "weird")
}

func (s *loggingSuite) TestNewReturnsLogger() {
	s.NotNil(New(false, "dev"))
}

func (s *loggingSuite) TestWithGroupEmptyIsNoop() {
	var buf bytes.Buffer
	h := newCustomHandler(&buf, false, "")
	s.Same(h, h.WithGroup(""))
}

func (s *loggingSuite) TestWithGroupNonEmptyReturnsNewHandler() {
	var buf bytes.Buffer
	h := newCustomHandler(&buf, false, "")
	s.NotSame(h, h.WithGroup("g"))
}
