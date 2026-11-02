package ebook

// ReaderVersion identifies the current reading logic. Bump it in the same
// change as a reader fix that could make a previously broken file readable.
// A stored broken-read record skips the read (ingest's readBook gate) only
// while its reader version equals ReaderVersion, so a bump re-checks every
// recorded book.
const ReaderVersion = 1
