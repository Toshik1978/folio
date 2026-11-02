package mcpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/suite"
)

func TestMcpserver(t *testing.T) {
	suite.Run(t, new(linksSuite))
	suite.Run(t, new(summarySuite))
	suite.Run(t, new(serverSuite))
	suite.Run(t, new(toolsSuite))
}

type linksSuite struct{ suite.Suite }

func (s *linksSuite) TestBookLink() {
	s.Equal("https://folio.example.com/books/42", bookLink("https://folio.example.com", 42))
	s.Equal("https://folio.example.com/books/42", bookLink("https://folio.example.com/", 42))
	s.Equal("/books/7", bookLink("", 7))
}

type summarySuite struct{ suite.Suite }

func (s *summarySuite) TestExcerptShortUntouched() {
	s.Equal("Hello world", excerpt("<p>Hello <b>world</b></p>"))
	s.Empty(excerpt(""))
}

func (s *summarySuite) TestExcerptCyrillicTruncation() {
	word := "приключение"
	text := strings.TrimSpace(strings.Repeat(word+" ", 60))
	got := excerpt("<p>" + text + "</p>")

	s.True(utf8.ValidString(got))
	s.True(strings.HasSuffix(got, "…"))
	body := strings.TrimSuffix(got, "…")
	s.LessOrEqual(utf8.RuneCountInString(body), 300)
	s.Greater(utf8.RuneCountInString(body), 250)
	for w := range strings.FieldsSeq(body) {
		s.Equal(word, w, "cut must fall on a word boundary")
	}
}

func (s *summarySuite) TestExcerptExactly300NotCut() {
	text := strings.Repeat("я", 300)
	s.Equal(text, excerpt(text))
}

func (s *summarySuite) TestExcerptNoSpaces() {
	got := excerpt(strings.Repeat("я", 400))
	s.Equal(strings.Repeat("я", 300)+"…", got)
}

func (s *summarySuite) TestNewSummary() {
	num := 2.5
	year := 1999
	got := newSummary("https://f.example", bookFields{
		ID: 5, Title: "T", Authors: nil, Series: "S", SeriesNumber: &num,
		Genres: []string{"sf"}, Language: "ru", Year: &year,
		Annotation: "<p>text</p>",
	})
	s.Equal(int64(5), got.ID)
	s.NotNil(got.Authors, "authors must serialize as [] not null")
	s.Equal("S", *got.Series)
	s.Equal("text", got.Excerpt)
	s.True(got.HasDescription)
	s.Equal("https://f.example/books/5", got.Link)

	empty := newSummary("", bookFields{ID: 1, Title: "T", Annotation: "<p> </p>"})
	s.False(empty.HasDescription)
	s.Empty(empty.Excerpt)
	s.Nil(empty.Series)
	s.NotNil(empty.Genres)
}

func (s *summarySuite) TestNewDetail() {
	got := newDetail("", bookFields{ID: 3, Title: "T", Annotation: "<p>full</p>"}, detailExtra{
		Publisher: "P", LibraryID: 1, LibraryName: "L",
		Identifiers: []Identifier{{Type: "isbn", Value: "1"}},
		Formats:     []string{"fb2"},
	})
	s.Equal("full", *got.Description)
	s.Equal("P", *got.Publisher)
	s.Equal("L", got.Library.Name)
	s.Equal("/books/3", got.Link)
	s.True(got.HasDescription)
}

type serverSuite struct{ suite.Suite }

func (s *serverSuite) TestInitializeAndListTools() {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	srv := newMCPServer(log, nil, "", "v1.2.3")

	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	s.Require().NoError(err)
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	s.Require().NoError(err)
	defer cs.Close()

	init := cs.InitializeResult()
	s.Equal("folio", init.ServerInfo.Name)
	s.Equal("Folio library", init.ServerInfo.Title)
	s.Equal("v1.2.3", init.ServerInfo.Version)
	s.Equal(serverInstructions, init.Instructions)
	s.Contains(init.Instructions, "Always give the user each book's `link`.")

	_, err = cs.ListTools(ctx, nil)
	s.Require().NoError(err)
}

func (s *serverSuite) TestNewReturnsHandler() {
	h := New(slog.New(slog.DiscardHandler), nil, "", "dev")
	s.NotNil(h)
}

// TestServedBehindLoopbackProxy confirms a request that reaches the handler over
// loopback with a public Host (cloudflared / nginx on the same host) is served,
// not rejected by the SDK's DNS-rebinding guard.
func (s *serverSuite) TestServedBehindLoopbackProxy() {
	ts := httptest.NewServer(New(slog.New(slog.DiscardHandler), nil, "", "dev"))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, strings.NewReader(body))
	s.Require().NoError(err)
	req.Host = "folio.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}
