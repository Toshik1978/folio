package ebook

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
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
}

func parseMOBI(_ context.Context, path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open mobi: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return Metadata{}, fmt.Errorf("stat mobi: %w", err)
	}

	mf, err := openMOBI(f, info.Size())
	if err != nil {
		return Metadata{}, err
	}

	m := mf.readTitle()
	// A missing/short EXTH is not fatal: the record-0 header title already read
	// stands. The title is entity-decoded below either way.
	mf.readEXTH(&m)

	// MOBI/AZW3 titles (record-0 header or EXTH 503) can carry HTML/numeric
	// entities (e.g. retail Kindle "Death&#x2019;s End"). Decode after EXTH so
	// the preferred EXTH 503 title is unescaped too — and so a file without an
	// EXTH block still gets its record-0 header title decoded.
	m.Title = html.UnescapeString(m.Title)

	return m, nil
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
	titleOffset := int(binary.BigEndian.Uint32(mf.rec0[84:88]))
	titleLength := int(binary.BigEndian.Uint32(mf.rec0[88:92]))

	var m Metadata
	if titleOffset+titleLength <= len(mf.rec0) && titleLength > 0 {
		m.Title = strings.TrimSpace(mf.decodeText(mf.rec0[titleOffset : titleOffset+titleLength]))
	}

	return m
}

// readEXTH applies the EXTH metadata block to m when one is present and well
// formed. It is best-effort: a missing/short/!magic EXTH simply leaves m with
// the record-0 header title already read, so the caller never has to special-case
// the failure (it just decodes entities on whatever title stands).
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

	coverOffset := mf.parseEXTHRecords(exth, m)
	if coverOffset >= 0 {
		m.Cover = mf.extractCover(coverOffset)
	}
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
			m.Identifiers = append(m.Identifiers, Identifier{Type: identifierISBN, Value: isbn})
		}
	}
}

func (mf *mobiFile) extractCover(coverOffset int) []byte {
	// EXTH 201 (CoverOffset) is relative to the MOBI header's FirstImageIndex.
	// This is the standard base for both MOBI6 and KF8 — confirmed by the
	// leotaku/mobi writer which sets:
	//   FirstImageIndex = PDB index of the first image record
	//   EXTHCoverOffset = position of cover within the image sequence
	if mf.firstImageIndex >= 0 {
		coverRec := mf.firstImageIndex + coverOffset
		if img := mf.readRecordImage(coverRec); img != nil {
			return img
		}
	}

	// Fallback: FirstImageIndex missing or resolved record not an image.
	// Scan for the first record with an image signature.
	firstImage := mf.findFirstImageRecord()
	if firstImage < 0 {
		return nil
	}

	return mf.readRecordImage(firstImage)
}

func (mf *mobiFile) findFirstImageRecord() int {
	// Read only each record's leading bytes — enough to match an image signature
	// — instead of whole records, so the scan stays cheap on large files.
	head := make([]byte, 4)
	for i := range mf.numRecords {
		recStart := mf.recOffsets[i]
		if recStart < 0 || recStart+4 > mf.size {
			continue
		}
		if _, err := mf.r.ReadAt(head, recStart); err != nil {
			continue
		}
		if isImageHeader(head) {
			return i
		}
	}

	return -1
}

func (mf *mobiFile) readRecordImage(recIndex int) []byte {
	if recIndex < 0 || recIndex >= mf.numRecords {
		return nil
	}

	recStart := mf.recOffsets[recIndex]
	recEnd := mf.size
	if recIndex+1 < mf.numRecords {
		recEnd = mf.recOffsets[recIndex+1]
	}

	if recStart < 0 || recStart >= recEnd || recStart >= mf.size {
		return nil
	}
	if recEnd > mf.size {
		recEnd = mf.size
	}

	coverData := make([]byte, recEnd-recStart)
	if _, err := mf.r.ReadAt(coverData, recStart); err != nil {
		return nil
	}
	if !isImageHeader(coverData) {
		return nil
	}

	return coverData
}

var imageSignatures = [][]byte{
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
