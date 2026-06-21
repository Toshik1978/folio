package ingest

import "testing"

func TestGroupKeyAuthorOrderIndependent(t *testing.T) {
	const title, lang = "Death's End", "en"
	surnameFirst := groupKey(title, []string{"Liu, Cixin"}, lang)
	firstLast := groupKey(title, []string{"Cixin Liu"}, lang)
	spaceOnly := groupKey(title, []string{"Liu Cixin"}, lang)

	if surnameFirst != firstLast {
		t.Fatalf("comma vs plain differ:\n %q\n %q", surnameFirst, firstLast)
	}
	if firstLast != spaceOnly {
		t.Fatalf("order differs:\n %q\n %q", firstLast, spaceOnly)
	}
}

func TestGroupKeyMultiAuthorOrderIndependent(t *testing.T) {
	a := groupKey("T", []string{"Smith, John", "Doe, Jane"}, "en")
	b := groupKey("T", []string{"Jane Doe", "John Smith"}, "en")
	if a != b {
		t.Fatalf("multi-author order not normalized:\n %q\n %q", a, b)
	}
}

func TestGroupKeyDifferentLanguageSplits(t *testing.T) {
	en := groupKey("T", []string{"A B"}, "en")
	ru := groupKey("T", []string{"A B"}, "ru")
	if en == ru {
		t.Fatal("different language must produce different key")
	}
}

func TestGroupKeyMiddleInitialStillSplits(t *testing.T) {
	// Accepted limitation: a differing middle token still splits (identifiers heal it).
	withInitial := groupKey("T", []string{"Ursula K. Le Guin"}, "en")
	without := groupKey("T", []string{"Ursula Le Guin"}, "en")
	if withInitial == without {
		t.Fatal("expected middle-initial difference to split (documented limitation)")
	}
}
