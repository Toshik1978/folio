package ebook

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/samber/lo"
	"golang.org/x/text/encoding/charmap"
)

const (
	exthAuthor       = 100
	exthPublisher    = 101
	exthDescription  = 103
	exthISBN         = 104
	exthPublishDate  = 106
	exthLanguage     = 524
	exthCoverOffset  = 201
	exthUpdatedTitle = 503

	mobiEncodingCP1252 = 1252
	mobiMagic          = "MOBI"
	exthMagic          = "EXTH"
	mobiMinSize        = 78
	mobiRec0MinSize    = 132
	mobiMaxRec0        = 1 << 20 // read cap for record 0 (the header block)
	mobiExthFlag       = 0x40
	recTableStart      = 78
	recEntrySize       = 8
	exthHeaderSize     = 12
	exthRecHeaderLen   = 8
)

type mobiFile struct {
	r               io.ReaderAt
	size            int64
	numRecords      int
	recOffsets      []int64 // PDB record start offsets, indexed by record number
	rec0            []byte
	isCP1252        bool
	firstImageIndex int // MOBI header "First Image Index" record; base for EXTH 201
	coverOffset     int // EXTH 201 value read by readEXTH; -1 when absent
}

func openMOBI(r io.ReaderAt, size int64) (mobiFile, error) {
	if size < mobiMinSize {
		return mobiFile{}, errors.New("mobi file too short")
	}

	header := make([]byte, mobiMinSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return mobiFile{}, fmt.Errorf("read mobi header: %w", err)
	}
	numRecords := int(binary.BigEndian.Uint16(header[76:78]))
	if size < int64(mobiMinSize)+int64(numRecords)*recEntrySize {
		return mobiFile{}, errors.New("mobi record table truncated")
	}

	recOffsets, err := readMOBIRecordTable(r, numRecords)
	if err != nil {
		return mobiFile{}, err
	}
	if len(recOffsets) == 0 {
		return mobiFile{}, errors.New("mobi has no records")
	}

	rec0, err := readMOBIRecord0(r, size, recOffsets)
	if err != nil {
		return mobiFile{}, err
	}

	if string(rec0[16:20]) != mobiMagic {
		return mobiFile{}, errors.New("MOBI magic not found")
	}

	encoding := int(binary.BigEndian.Uint32(rec0[28:32]))

	// MOBI header "First Image Index" (MobileRead MOBI header field at offset
	// 0x6C/108 from the start of record 0, i.e. PalmDOC header (16 bytes) + MOBI
	// header offset 0x5C/92) → rec0[108:112]. See the field table at
	// https://wiki.mobileread.com/wiki/MOBI#MOBI_Header. EXTH 201 (CoverOffset)
	// is relative to this record index. The value is 0xFFFFFFFF when no images
	// are present; treat that as "not set".
	firstImageIndex := -1
	if len(rec0) >= 112 {
		v := binary.BigEndian.Uint32(rec0[108:112])
		if v != 0xFFFFFFFF {
			firstImageIndex = int(v)
		}
	}

	return mobiFile{
		r:               r,
		size:            size,
		numRecords:      numRecords,
		recOffsets:      recOffsets,
		rec0:            rec0,
		isCP1252:        encoding == mobiEncodingCP1252,
		firstImageIndex: firstImageIndex,
		coverOffset:     -1,
	}, nil
}

// readMOBIRecordTable reads the PDB record table (numRecords 8-byte entries that
// follow the 78-byte header) and returns each record's start offset.
func readMOBIRecordTable(r io.ReaderAt, numRecords int) ([]int64, error) {
	table := make([]byte, numRecords*recEntrySize)
	if _, err := r.ReadAt(table, recTableStart); err != nil {
		return nil, fmt.Errorf("read mobi record table: %w", err)
	}

	offsets := make([]int64, numRecords)
	for i := range numRecords {
		offsets[i] = int64(binary.BigEndian.Uint32(table[i*recEntrySize : i*recEntrySize+4]))
	}

	return offsets, nil
}

// readMOBIRecord0 reads only record 0 (the PalmDOC + MOBI + EXTH header), bounded
// by the next record's offset, so the book's text records are never loaded.
func readMOBIRecord0(r io.ReaderAt, size int64, recOffsets []int64) ([]byte, error) {
	rec0Offset := recOffsets[0]
	if rec0Offset < 0 || rec0Offset >= size {
		return nil, errors.New("invalid record 0 offset")
	}

	rec0End := size
	if len(recOffsets) > 1 && recOffsets[1] > rec0Offset && recOffsets[1] <= size {
		rec0End = recOffsets[1]
	}
	if rec0End-rec0Offset < mobiRec0MinSize {
		return nil, errors.New("record 0 too short for MOBI header")
	}
	// size can be an archive's declared (attacker-controlled) entry size, and a
	// one-record file would otherwise size record 0 to the whole book; the
	// PalmDOC + MOBI + EXTH header always fits well inside the cap.
	rec0End = min(rec0End, rec0Offset+mobiMaxRec0)

	rec0 := make([]byte, rec0End-rec0Offset)
	if _, err := r.ReadAt(rec0, rec0Offset); err != nil {
		return nil, fmt.Errorf("read mobi record 0: %w", err)
	}

	return rec0, nil
}

func (mf *mobiFile) decodeText(b []byte) string {
	if mf.isCP1252 {
		decoded, err := charmap.Windows1252.NewDecoder().Bytes(b)
		if err == nil {
			return string(decoded)
		}
	}

	return string(b)
}

func (mf *mobiFile) readTitle() Metadata {
	// Use uint64 arithmetic so a uint32 value with the high bit set (e.g.
	// 0x80000000) never becomes a negative int on 32-bit builds and silently
	// defeats the bounds guard. All comparisons stay unsigned regardless of
	// the platform's int width.
	titleOffset := uint64(binary.BigEndian.Uint32(mf.rec0[84:88]))
	titleLength := uint64(binary.BigEndian.Uint32(mf.rec0[88:92]))

	var m Metadata
	if titleLength > 0 && titleOffset+titleLength <= uint64(len(mf.rec0)) {
		m.Title = strings.TrimSpace(mf.decodeText(mf.rec0[titleOffset : titleOffset+titleLength]))
	}

	return m
}

// readEXTH applies the EXTH metadata block to m when one is present and well
// formed. It is best-effort: a missing/short/!magic EXTH simply leaves m with
// the record-0 header title already read, so the caller never has to special-case
// the failure (it just decodes entities on whatever title stands). The EXTH 201
// cover offset is stored on mf.coverOffset (-1 when absent), not resolved here.
func (mf *mobiFile) readEXTH(m *Metadata) {
	exthFlag := binary.BigEndian.Uint32(mf.rec0[128:132])
	if exthFlag&mobiExthFlag == 0 {
		return
	}

	mobiHeaderLen := int(binary.BigEndian.Uint32(mf.rec0[20:24]))
	if mobiHeaderLen < mobiRec0MinSize-16 {
		return
	}
	exthStart := 16 + mobiHeaderLen
	if exthStart+exthHeaderSize > len(mf.rec0) {
		return
	}

	exth := mf.rec0[exthStart:]
	if string(exth[:4]) != exthMagic {
		return
	}

	mf.coverOffset = mf.parseEXTHRecords(exth, m)
}

// mobiReadMetadata reads the title and EXTH block of src without resolving the
// cover image.
func mobiReadMetadata(ctx context.Context, src Source) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("mobi metadata: %w", err)
	}
	mf, err := openMOBI(src.R, src.Size)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: mobi metadata: %w", ErrMalformed, err)
	}

	m := mf.readTitle()
	mf.readEXTH(&m)
	m.Title = html.UnescapeString(m.Title)

	return m, nil
}

// mobiReadCover resolves the EXTH 201 cover of src, or (nil, nil) when it has none.
func mobiReadCover(ctx context.Context, src Source) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mobi cover: %w", err)
	}
	mf, err := openMOBI(src.R, src.Size)
	if err != nil {
		return nil, fmt.Errorf("%w: mobi cover: %w", ErrMalformed, err)
	}

	var discard Metadata
	mf.readEXTH(&discard)
	if mf.coverOffset < 0 {
		return nil, nil
	}
	// EXTH 201 present but pointing outside the records is "no cover", not a
	// cue for the first-image scan (that fallback is only for an absent 201).
	// A FirstImageIndex outside the records is a broken base, not a broken
	// offset, so it still gets extractCover's first-image scan.
	if mf.firstImageIndex >= 0 && mf.firstImageIndex < mf.numRecords {
		if rec := mf.firstImageIndex + mf.coverOffset; rec < 0 || rec >= mf.numRecords {
			return nil, nil
		}
	}
	cover, err := mf.extractCover(ctx, mf.coverOffset)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mobi cover: %w", err)
	}

	return cover, nil
}

func (mf *mobiFile) parseEXTHRecords(exth []byte, m *Metadata) int {
	count := int(binary.BigEndian.Uint32(exth[8:12]))
	pos := exthHeaderSize
	coverOffset := -1

	for range count {
		if pos+exthRecHeaderLen > len(exth) {
			break
		}
		recType := int(binary.BigEndian.Uint32(exth[pos : pos+4]))
		recLen := int(binary.BigEndian.Uint32(exth[pos+4 : pos+8]))
		if recLen < exthRecHeaderLen || pos+recLen > len(exth) {
			break
		}

		if recType == exthCoverOffset && recLen == exthHeaderSize {
			coverOffset = int(binary.BigEndian.Uint32(exth[pos+8 : pos+12]))
		} else {
			applyEXTHRecord(recType, mf.decodeText(exth[pos+8:pos+recLen]), m)
		}

		pos += recLen
	}

	return coverOffset
}

func applyEXTHRecord(recType int, val string, m *Metadata) {
	switch recType {
	case exthAuthor:
		if author := strings.TrimSpace(val); author != "" {
			m.Authors = append(m.Authors, author)
		}
	case exthUpdatedTitle:
		// EXTH 503 is the clean, full title; prefer it over the record-0 header
		// title, which on retail Kindle files is entity-encoded and/or truncated.
		if title := strings.TrimSpace(val); title != "" {
			m.Title = title
		}
	case exthDescription:
		m.Annotation = lo.CoalesceOrEmpty(m.Annotation, strings.TrimSpace(val))
	case exthLanguage:
		m.Language = lo.CoalesceOrEmpty(m.Language, strings.TrimSpace(val))
	default:
		applyEXTHPublishing(recType, val, m)
	}
}

// applyEXTHPublishing handles the publishing-metadata EXTH records, split out to
// keep applyEXTHRecord's branching low.
func applyEXTHPublishing(recType int, val string, m *Metadata) {
	switch recType {
	case exthPublisher:
		m.Publisher = lo.CoalesceOrEmpty(m.Publisher, strings.TrimSpace(val))
	case exthPublishDate:
		m.Year = lo.CoalesceOrEmpty(m.Year, ParseYear(val))
	case exthISBN:
		if isbn := strings.TrimSpace(val); isbn != "" {
			m.Identifiers = append(m.Identifiers, Identifier{Type: IdentifierISBN, Value: isbn})
		}
	}
}

// extractCover returns the cover image record, or (nil, nil) when the records
// read cleanly but hold none. A failed read is returned (see coverReadError).
func (mf *mobiFile) extractCover(ctx context.Context, coverOffset int) ([]byte, error) {
	// EXTH 201 (CoverOffset) is relative to the MOBI header's FirstImageIndex.
	// This is the standard base for both MOBI6 and KF8 — confirmed by the
	// leotaku/mobi writer which sets:
	//   FirstImageIndex = PDB index of the first image record
	//   EXTHCoverOffset = position of cover within the image sequence
	if mf.firstImageIndex >= 0 {
		coverRec := mf.firstImageIndex + coverOffset
		img, err := mf.readRecordImage(coverRec)
		if err != nil || img != nil {
			return img, err
		}
	}

	// Fallback: FirstImageIndex missing or resolved record not an image.
	// Scan for the first record with an image signature.
	firstImage, err := mf.findFirstImageRecord(ctx)
	if err != nil || firstImage < 0 {
		return nil, err
	}

	return mf.readRecordImage(firstImage)
}

// findFirstImageRecord returns the index of the first record with an image
// signature, or -1 when there is none. A failed read or a canceled ctx is
// returned (see coverReadError).
func (mf *mobiFile) findFirstImageRecord(ctx context.Context) (int, error) {
	// Read only each record's leading bytes — enough to match an image signature
	// — instead of whole records, so the scan stays cheap on large files.
	head := make([]byte, 4)
	for i := range mf.numRecords {
		// The scan can touch up to 65535 records; abandon it if the caller cancels.
		if err := ctx.Err(); err != nil {
			return -1, fmt.Errorf("mobi cover: %w", err)
		}
		recStart := mf.recOffsets[i]
		if recStart < 0 || recStart+4 > mf.size {
			continue
		}
		if _, err := mf.r.ReadAt(head, recStart); err != nil {
			return -1, coverReadError(err)
		}
		if isImageHeader(head) {
			return i, nil
		}
	}

	return -1, nil
}

// readRecordImage returns record recIndex when it holds an image, or (nil, nil)
// when it is out of range or reads cleanly but is not an image. A failed read
// is returned (see coverReadError).
func (mf *mobiFile) readRecordImage(recIndex int) ([]byte, error) {
	recStart, recLen, err := mf.recordBounds(recIndex)
	if err != nil || recLen == 0 {
		return nil, err
	}

	coverData := make([]byte, recLen)
	if _, err := mf.r.ReadAt(coverData, recStart); err != nil {
		return nil, coverReadError(err)
	}
	if !isImageHeader(coverData) {
		return nil, nil
	}

	return coverData, nil
}

// coverReadError classifies a failed cover-record read so the Dispatcher can
// act on it: an error already matching ErrSourceIO, ErrTooLarge or ErrMalformed
// is kept as is; any other (for example a short read of a record the file
// declares) means the file is malformed.
func coverReadError(err error) error {
	if errors.Is(err, ErrSourceIO) || errors.Is(err, ErrTooLarge) || errors.Is(err, ErrMalformed) {
		return fmt.Errorf("mobi cover: %w", err)
	}

	return fmt.Errorf("%w: mobi cover: %w", ErrMalformed, err)
}

// recordBounds resolves the [start, start+len) byte range of PDB record
// recIndex, returning length 0 when the record is out of range or the offsets are
// inconsistent. A span exceeding maxCoverBytes is an ErrTooLarge error, not a
// missing record: the size cap bounds the allocation against a
// corrupt/pathological record table rather than trusting the offsets (mirrors
// the archive read caps), and the file is classified like an oversized cover in
// any other format.
func (mf *mobiFile) recordBounds(recIndex int) (start, length int64, err error) {
	if recIndex < 0 || recIndex >= mf.numRecords {
		return 0, 0, nil
	}

	recStart := mf.recOffsets[recIndex]
	recEnd := mf.size
	if recIndex+1 < mf.numRecords {
		recEnd = mf.recOffsets[recIndex+1]
	}
	if recEnd > mf.size {
		recEnd = mf.size
	}

	if recStart < 0 || recStart >= recEnd {
		return 0, 0, nil
	}
	if recEnd-recStart > maxCoverBytes {
		return 0, 0, fmt.Errorf("%w: mobi cover record is larger than %d bytes", ErrTooLarge, maxCoverBytes)
	}

	return recStart, recEnd - recStart, nil
}

var imageSignatures = [][]byte{ //nolint:gochecknoglobals // read-only lookup table
	{0xFF, 0xD8, 0xFF},    // JPEG
	{0x89, 'P', 'N', 'G'}, // PNG
	{'G', 'I', 'F'},       // GIF
	{'B', 'M'},            // BMP
}

func isImageHeader(data []byte) bool {
	for _, sig := range imageSignatures {
		if len(data) >= len(sig) && bytes.Equal(data[:len(sig)], sig) {
			return true
		}
	}

	return false
}
