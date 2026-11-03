package ebook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

type dispatchSuite struct {
	baseSuite
}

// panicParser is a Parser whose operations always panic, used to prove the
// dispatcher's recover wrapper turns a panic into an error.
type panicParser struct{}

func (panicParser) Extensions() []string                               { return []string{".boom"} }
func (panicParser) Traits() Traits                                     { return Traits{} }
func (panicParser) Metadata(context.Context, Source) (Metadata, error) { panic("kaboom") }
func (panicParser) Cover(context.Context, Source) ([]byte, error)      { panic("kaboom") }

// fakeParser returns err from every operation and counts how often it ran,
// used to prove the dispatcher's error classification and ctx short-circuit.
type fakeParser struct {
	err   error
	calls int
}

func (*fakeParser) Extensions() []string { return []string{".fake"} }
func (*fakeParser) Traits() Traits       { return Traits{} }

func (f *fakeParser) Metadata(context.Context, Source) (Metadata, error) {
	f.calls++
	return Metadata{}, f.err
}

func (f *fakeParser) Cover(context.Context, Source) ([]byte, error) {
	f.calls++
	return nil, f.err
}

func (s *dispatchSuite) TestMetadataAndCoverRecoverFromPanic() {
	d := NewDispatcher(panicParser{})
	src := Source{Name: "x.boom", R: strings.NewReader(""), Size: 0}

	_, err := d.Metadata(context.Background(), s.log, src)
	var pe *PanicError
	s.Require().ErrorAs(err, &pe)
	s.Equal("kaboom", pe.Value)
	reason, ok := ReasonOf(err)
	s.True(ok)
	s.Equal(ReasonCrashed, reason)

	_, err = d.Cover(context.Background(), s.log, src)
	s.Require().ErrorAs(err, &pe)
	reason, ok = ReasonOf(err)
	s.True(ok)
	s.Equal(ReasonCrashed, reason)
}

func (s *dispatchSuite) TestCancelledContextNeverCallsParser() {
	f := &fakeParser{}
	d := NewDispatcher(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := Source{Name: "x.fake", R: strings.NewReader(""), Size: 0}

	_, err := d.Metadata(ctx, s.log, src)
	s.Require().ErrorIs(err, context.Canceled)
	_, err = d.Cover(ctx, s.log, src)
	s.Require().ErrorIs(err, context.Canceled)
	s.Zero(f.calls)
}

func (s *dispatchSuite) TestErrorClassification() {
	generic := errors.New("bad header")
	tests := []struct {
		name       string
		err        error
		malformed  bool
		wantReason Reason
		wantOK     bool
	}{
		{"source io passes through", ErrSourceIO, false, "", false},
		{"too large passes through", ErrTooLarge, false, ReasonTooLarge, true},
		{"canceled passes through", context.Canceled, false, "", false},
		{"deadline passes through", context.DeadlineExceeded, false, "", false},
		{"generic becomes malformed", generic, true, ReasonCorrupt, true},
		{"malformed passes through", fmt.Errorf("%w: bad header", ErrMalformed), true, ReasonCorrupt, true},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			d := NewDispatcher(&fakeParser{err: tt.err})
			src := Source{Name: "x.fake", R: strings.NewReader(""), Size: 0}
			_, mErr := d.Metadata(context.Background(), s.log, src)
			_, cErr := d.Cover(context.Background(), s.log, src)
			for _, err := range []error{mErr, cErr} {
				s.Require().ErrorIs(err, tt.err)
				s.Equal(tt.malformed, errors.Is(err, ErrMalformed))
				reason, ok := ReasonOf(err)
				s.Equal(tt.wantOK, ok)
				s.Equal(tt.wantReason, reason)
				s.LessOrEqual(strings.Count(err.Error(), ErrMalformed.Error()), 1, "ErrMalformed wrapped twice")
			}
		})
	}
}

// TestRealParserPlainErrorIsMalformed proves a parser's plain (unsentinelled)
// error — here zip.NewReader rejecting a non-zip — classifies as corrupt only
// because the Dispatcher wraps it.
func (s *dispatchSuite) TestRealParserPlainErrorIsMalformed() {
	data := []byte("this is not a zip archive")
	src := Source{Name: "broken.epub", R: strings.NewReader(string(data)), Size: int64(len(data))}

	_, err := s.d.Metadata(context.Background(), s.log, src)
	s.Require().ErrorIs(err, ErrMalformed)
	reason, ok := ReasonOf(err)
	s.True(ok)
	s.Equal(ReasonCorrupt, reason)
}

// TestRealParserMalformedWrappedOnce proves a parser error that already matches
// ErrMalformed is not wrapped a second time by the Dispatcher.
func (s *dispatchSuite) TestRealParserMalformedWrappedOnce() {
	data := []byte("short")
	for _, name := range []string{"bad.pdf", "bad.mobi"} {
		src := Source{Name: name, R: strings.NewReader(string(data)), Size: int64(len(data))}
		_, err := s.d.Metadata(context.Background(), s.log, src)
		s.Require().ErrorIs(err, ErrMalformed, name)
		s.Equal(1, strings.Count(err.Error(), ErrMalformed.Error()), err.Error())
	}
}

func (s *dispatchSuite) TestSourceUnsupportedFormat() {
	for _, name := range []string{"x.xyz", "something.zip", "no_ext"} {
		s.Run(name, func() {
			src := Source{Name: name, R: strings.NewReader(""), Size: 0}
			_, err := s.d.Metadata(context.Background(), s.log, src)
			s.Require().ErrorContains(err, "unsupported format")
			s.Require().ErrorContains(err, name)
			_, err = s.d.Cover(context.Background(), s.log, src)
			s.Require().ErrorContains(err, "unsupported format")
			s.Require().ErrorContains(err, name)
		})
	}
}

func (s *dispatchSuite) TestSourceRoutesByExtension() {
	tests := []struct {
		file  string
		title string
	}{
		{"test.epub", "Test EPUB Book"},
		{"test.fb2", "Test FB2 Book"},
		{"test.fb2.zip", "Test FB2 Book"},
		{"test.mobi", "Test MOBI Book"},
		{"test.azw3", "Test AZW3 Book"},
		{"test.pdf", "Test PDF Book"},
	}

	for _, tt := range tests {
		s.Run(tt.file, func() {
			m, err := s.d.Metadata(context.Background(), s.log, s.open(s.fixture(tt.file)))
			s.Require().NoError(err)
			s.Equal(tt.title, m.Title)

			c, err := s.d.Cover(context.Background(), s.log, s.open(s.fixture(tt.file)))
			s.Require().NoError(err)
			s.NotEmpty(c)
		})
	}
}

func (s *dispatchSuite) TestRankAndDescribes() {
	d := NewStandardDispatcher()
	s.Greater(d.Rank("epub"), d.Rank("fb2"))
	s.Greater(d.Rank("fb2"), d.Rank("mobi"))
	s.Equal(d.Rank("mobi"), d.Rank("azw3"))
	s.Equal(d.Rank("mobi"), d.Rank("azw"))
	s.Greater(d.Rank("azw3"), d.Rank("pdf"))
	s.Positive(d.Rank("pdf"))
	s.Zero(d.Rank("xyz"))
	s.Zero(d.Rank(""))

	s.True(d.Describes("epub"))
	s.True(d.Describes("fb2"))
	s.True(d.Describes("azw3"))
	s.False(d.Describes("pdf"))
	s.False(d.Describes("xyz"))
}

func (s *dispatchSuite) TestExtensions() {
	got := NewStandardDispatcher().Extensions()
	s.Equal([]string{".azw", ".azw3", ".epub", ".fb2", ".fb2.zip", ".mobi", ".pdf"}, got)
	s.True(slices.IsSorted(got))
}

func (s *dispatchSuite) TestContentType() {
	tests := map[string]string{
		"epub":    "application/epub+zip",
		"fb2":     "application/x-fictionbook+xml",
		"mobi":    "application/x-mobipocket-ebook",
		"azw":     "application/x-mobipocket-ebook",
		"azw3":    "application/x-mobipocket-ebook",
		"pdf":     "application/pdf",
		"EPUB":    "application/epub+zip",
		"unknown": "application/octet-stream",
		"":        "application/octet-stream",
	}
	for format, want := range tests {
		s.Equal(want, ContentType(format), format)
	}
}

// TestSourceIOFromFileIsRetryable proves a read error from an OpenFile source
// reaches the caller as ErrSourceIO, not as malformed content.
func (s *dispatchSuite) TestSourceIOFromFileIsRetryable() {
	src, c, err := OpenFile(s.fixture("test.epub"))
	s.Require().NoError(err)
	f, ok := c.(*os.File)
	s.Require().True(ok)
	s.Require().NoError(f.Close()) // reads now fail on the closed descriptor

	_, err = s.d.Metadata(context.Background(), s.log, src)
	s.Require().ErrorIs(err, ErrSourceIO)
	_, ok = ReasonOf(err)
	s.False(ok)
}

func (s *dispatchSuite) TestNewDispatcherPanicsOnDuplicateExtension() {
	s.PanicsWithValue(`ebook: duplicate parser for extension ".epub"`, func() {
		NewDispatcher(epubParser{}, epubParser{})
	})
}

func (s *dispatchSuite) TestFileExtMultipleDots() {
	tests := []struct {
		path string
		want string
	}{
		{"book.epub", ".epub"},
		{"book.fb2", ".fb2"},
		{"book.fb2.zip", extFB2Zip},
		{"author.name.fb2.zip", extFB2Zip},
		{"tolstoy.war_and_peace.fb2.zip", extFB2Zip},
		{"my.book.v2.mobi", ".mobi"},
		{"no_ext", ""},
		{"unknown.xyz", ".xyz"},
	}

	for _, tt := range tests {
		s.Run(tt.path, func() {
			s.Equal(tt.want, s.d.fileExt(tt.path))
		})
	}
}

func (s *dispatchSuite) TestSupportedAndFormat() {
	s.True(s.d.Supported("book.epub"))
	s.True(s.d.Supported("book.FB2.ZIP"))
	s.False(s.d.Supported("book.xyz"))
	s.False(s.d.Supported("no_ext"))

	s.Equal("epub", s.d.Format("book.epub"))
	s.Equal("fb2", s.d.Format("book.fb2.zip"))
	s.Equal("mobi", s.d.Format("book.mobi"))
	s.Empty(s.d.Format("book.xyz"))
}

// recordingReporter captures what the dispatcher hands a CrashReporter.
type recordingReporter struct {
	place, detail string
	recovered     any
	stack         []byte
	calls         int
}

func (r *recordingReporter) ReportCrash(place, detail string, recovered any, stack []byte) bool {
	r.calls++
	r.place, r.detail, r.recovered, r.stack = place, detail, recovered, stack

	return true
}

func (s *dispatchSuite) TestPanicReportsCrash() {
	rep := &recordingReporter{}
	d := NewDispatcher(panicParser{})
	WithCrashReporter(rep)(d)
	src := Source{Name: "secret-title.boom", R: strings.NewReader(""), Size: 0}

	_, err := d.Metadata(context.Background(), s.log, src)
	var pe *PanicError
	s.Require().ErrorAs(err, &pe)
	s.Equal(1, rep.calls)
	s.Equal("book-file reading", rep.place)
	s.Contains(rep.detail, "boom parser")
	s.Contains(rep.detail, "metadata read")
	s.NotContains(rep.detail, "secret-title")
	s.Equal("kaboom", rep.recovered)
	s.NotEmpty(rep.stack)

	_, err = d.Cover(context.Background(), s.log, src)
	s.Require().ErrorAs(err, &pe)
	s.Equal(2, rep.calls)
	s.Contains(rep.detail, "cover read")
	s.NotContains(rep.detail, "secret-title")
}

func (s *dispatchSuite) TestNoReporterStillRecovers() {
	d := NewDispatcher(panicParser{})
	src := Source{Name: "x.boom", R: strings.NewReader(""), Size: 0}

	_, err := d.Metadata(context.Background(), s.log, src)
	var pe *PanicError
	s.Require().ErrorAs(err, &pe)
}

func (s *dispatchSuite) TestStandardDispatcherAcceptsReporter() {
	s.NotNil(NewStandardDispatcher(WithCrashReporter(&recordingReporter{})))
	s.NotNil(NewStandardDispatcher())
}
