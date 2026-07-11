package ebook

import (
	"context"
)

type dispatchSuite struct {
	baseSuite
}

// panicParser is a Parser whose Parse always panics, used to prove the
// dispatcher's recover wrapper turns a panic into an error.
type panicParser struct{}

func (panicParser) Extensions() []string                            { return []string{".boom"} }
func (panicParser) Parse(context.Context, string) (Metadata, error) { panic("kaboom") }

func (s *dispatchSuite) TestParseRecoversFromPanic() {
	d := NewDispatcher(panicParser{})
	_, err := d.Parse(context.Background(), s.log, "x.boom")
	s.Require().Error(err)
	s.Contains(err.Error(), "panicked")
}

func (s *dispatchSuite) TestNewDispatcherPanicsOnDuplicateExtension() {
	s.PanicsWithValue(`ebook: duplicate parser for extension ".epub"`, func() {
		NewDispatcher(NewEPUB(), NewEPUB())
	})
}

// TestParseHonorsCancelledContext proves the dispatcher bails before parsing
// when the caller's context is already cancelled, so a shutdown drains the
// parse queue instead of parsing every remaining file.
func (s *dispatchSuite) TestParseHonorsCancelledContext() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.d.Parse(ctx, s.log, s.fixture("test.epub"))
	s.Require().Error(err)
	s.ErrorIs(err, context.Canceled)
}

func (s *dispatchSuite) TestUnsupportedFormat() {
	_, err := s.d.Parse(context.Background(), s.log, "test.xyz")
	s.ErrorContains(err, "unsupported format")
}

func (s *dispatchSuite) TestUnsupportedZip() {
	_, err := s.d.Parse(context.Background(), s.log, "something.zip")
	s.ErrorContains(err, "unsupported format")
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

func (s *dispatchSuite) TestDispatchesByExtension() {
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
			m, err := s.d.Parse(context.Background(), s.log, s.fixture(tt.file))
			s.Require().NoError(err)
			s.Equal(tt.title, m.Title)
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
