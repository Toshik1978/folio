package amazon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Toshik1978/folio/internal/metasearch"
)

func rc(title, full string) rawCandidate {
	return rawCandidate{
		cover: metasearch.CoverCandidate{Source: metasearch.SourceAmazon, FullURL: full},
		title: title,
	}
}

func fullURLs(cands []metasearch.CoverCandidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.FullURL
	}
	return out
}

func TestFilterByTitleKeepsMatchesDropsNoise(t *testing.T) {
	in := []rawCandidate{
		rc("The Proving Ground: A Lincoln Lawyer Novel", "a.jpg"),
		rc("Nightshade", "b.jpg"),
		rc("Proving Ground: The Untold Story", "c.jpg"),
		rc("Self-Compassion: The Proven Power", "d.jpg"),
	}
	got := filterByTitle(in, "The Proving Ground", maxCandidates)
	// "the" is a stopword; "proving" + "ground" must both be present.
	require.Equal(t, []string{"a.jpg", "c.jpg"}, fullURLs(got))
}

func TestFilterByTitleDropsJunkEditions(t *testing.T) {
	in := []rawCandidate{
		rc("Dune", "a.jpg"),
		rc("Dune [Audiobook]", "b.jpg"),
		rc("Dune (Audio CD)", "c.jpg"),
	}
	got := filterByTitle(in, "Dune", maxCandidates)
	require.Equal(t, []string{"a.jpg"}, fullURLs(got))
}

func TestFilterByTitleCapsCount(t *testing.T) {
	in := make([]rawCandidate, 0, 10)
	for i := range 10 {
		in = append(in, rc("Dune", string(rune('a'+i))+".jpg"))
	}
	got := filterByTitle(in, "Dune", maxCandidates)
	require.Len(t, got, maxCandidates)
}

func TestFilterByTitleFailsOpenWithoutSignal(t *testing.T) {
	in := []rawCandidate{rc("Anything", "a.jpg"), rc("Other", "b.jpg")}
	// Empty query title: cannot filter, keep top-N.
	require.Equal(t, []string{"a.jpg", "b.jpg"}, fullURLs(filterByTitle(in, "", maxCandidates)))

	// No candidate carries a title (markup drift): keep top-N rather than go dark.
	noTitle := []rawCandidate{rc("", "a.jpg"), rc("", "b.jpg")}
	require.Equal(t, []string{"a.jpg", "b.jpg"}, fullURLs(filterByTitle(noTitle, "Dune", maxCandidates)))
}
