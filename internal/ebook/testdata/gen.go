//go:build ignore

package main

import (
	"archive/zip"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	generateEPUB(dir)
	generateFB2(dir)
	generateFB2Zip(dir)
	generateFB2Win1251(dir)
	generateFB2NoCover(dir)
	generateMOBI(dir)
	generateMOBICP1252(dir)
	generateAZW3(dir)
	generatePDF(dir)
	fmt.Println("all fixtures generated")
}

func generateEPUB(dir string) {
	path := filepath.Join(dir, "test.epub")
	f, err := os.Create(path)
	must(err)
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	writeZipEntry(zw, "mimetype", "application/epub+zip")

	writeZipEntry(zw, "META-INF/container.xml", `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)

	writeZipEntry(zw, "OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test EPUB Book</dc:title>
    <dc:creator>Jane Author</dc:creator>
    <dc:creator>John Coauthor</dc:creator>
    <dc:description>A test book for unit testing.</dc:description>
    <dc:subject>Science Fiction</dc:subject>
    <dc:subject>Adventure</dc:subject>
    <dc:language>en</dc:language>
    <meta name="cover" content="cover-img"/>
    <meta name="calibre:series" content="Test Series"/>
  </metadata>
  <manifest>
    <item id="cover-img" href="cover.png" media-type="image/png"/>
  </manifest>
</package>`)

	writeZipEntry(zw, "OEBPS/cover.png", fakePNG())
}

func generateFB2(dir string) {
	cover := base64.StdEncoding.EncodeToString([]byte(fakePNG()))

	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>Test FB2 Book</book-title>
      <genre>sf_history</genre>
      <genre>adventure</genre>
      <author>
        <first-name>Ivan</first-name>
        <middle-name>P.</middle-name>
        <last-name>Testov</last-name>
      </author>
      <annotation><p>An annotation for testing.</p></annotation>
      <sequence name="FB2 Series"/>
      <lang>ru</lang>
      <coverpage><image href="#cover.png"/></coverpage>
    </title-info>
  </description>
  <binary id="cover.png" content-type="image/png">%s</binary>
</FictionBook>`, cover)

	must(os.WriteFile(filepath.Join(dir, "test.fb2"), []byte(xml), 0o644))
}

func generateFB2Zip(dir string) {
	fb2Data, err := os.ReadFile(filepath.Join(dir, "test.fb2"))
	must(err)

	path := filepath.Join(dir, "test.fb2.zip")
	f, err := os.Create(path)
	must(err)
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	writeZipEntry(zw, "test.fb2", string(fb2Data))
}

func generateMOBI(dir string) {
	coverPNG := []byte(fakePNG())

	// Record 0 = header, record 1 = cover image → firstImageIndex = 1, coverOffset = 0.
	rec0 := buildMOBIRecord0("Test MOBI Book", "Alex Writer", "A MOBI description.", 65001, 1, 0)
	rec1 := coverPNG

	numRecords := 2
	recTableSize := numRecords * 8
	rec0Offset := 78 + recTableSize
	rec1Offset := rec0Offset + len(rec0)

	header := make([]byte, 78)
	copy(header[0:32], padTo("Test MOBI", 32))
	binary.BigEndian.PutUint16(header[76:78], uint16(numRecords))

	recTable := make([]byte, recTableSize)
	binary.BigEndian.PutUint32(recTable[0:4], uint32(rec0Offset))
	binary.BigEndian.PutUint32(recTable[8:12], uint32(rec1Offset))

	var buf []byte
	buf = append(buf, header...)
	buf = append(buf, recTable...)
	buf = append(buf, rec0...)
	buf = append(buf, rec1...)

	must(os.WriteFile(filepath.Join(dir, "test.mobi"), buf, 0o644))
}

func generateMOBICP1252(dir string) {
	enc := charmap.Windows1252.NewEncoder()
	cp1252 := func(s string) []byte {
		b, err := enc.Bytes([]byte(s))
		must(err)
		return b
	}

	rec0 := buildMOBIRecord0CP1252(
		cp1252("Résumé"),
		cp1252("François Müller"),
		cp1252("Découverte café."),
	)

	numRecords := 1
	recTableSize := numRecords * 8
	rec0Offset := 78 + recTableSize

	header := make([]byte, 78)
	copy(header[0:32], padTo("CP1252 MOBI", 32))
	binary.BigEndian.PutUint16(header[76:78], uint16(numRecords))

	recTable := make([]byte, recTableSize)
	binary.BigEndian.PutUint32(recTable[0:4], uint32(rec0Offset))

	var buf []byte
	buf = append(buf, header...)
	buf = append(buf, recTable...)
	buf = append(buf, rec0...)

	must(os.WriteFile(filepath.Join(dir, "test_cp1252.mobi"), buf, 0o644))
}

func buildMOBIRecord0CP1252(title, author, desc []byte) []byte {
	exthRecords := []exthRec{
		{typ: 100, data: author},
		{typ: 103, data: desc},
	}

	exthBody := buildEXTH(exthRecords)

	mobiHeaderLen := 116
	rec0 := make([]byte, 16+mobiHeaderLen)

	copy(rec0[16:20], "MOBI")
	binary.BigEndian.PutUint32(rec0[20:24], uint32(mobiHeaderLen))
	binary.BigEndian.PutUint32(rec0[28:32], 1252)
	binary.BigEndian.PutUint32(rec0[108:112], 0xFFFFFFFF) // no images
	binary.BigEndian.PutUint32(rec0[128:132], 0x40)

	titleOffset := len(rec0) + len(exthBody)

	binary.BigEndian.PutUint32(rec0[84:88], uint32(titleOffset))
	binary.BigEndian.PutUint32(rec0[88:92], uint32(len(title)))

	rec0 = append(rec0, exthBody...)
	rec0 = append(rec0, title...)

	return rec0
}

func buildMOBIRecord0(title, author, desc string, encoding uint32, firstImageIndex, coverOffset uint32) []byte {
	titleBytes := []byte(title)

	exthRecords := []exthRec{
		{typ: 100, data: []byte(author)},
		{typ: 103, data: []byte(desc)},
		{typ: 201, data: uint32Bytes(coverOffset)},
	}

	exthBody := buildEXTH(exthRecords)

	mobiHeaderLen := 116
	rec0 := make([]byte, 16+mobiHeaderLen)

	copy(rec0[16:20], "MOBI")
	binary.BigEndian.PutUint32(rec0[20:24], uint32(mobiHeaderLen))
	binary.BigEndian.PutUint32(rec0[28:32], encoding)
	// MOBI header "First Image Index" at PalmDoc(16) + MOBI offset 92 = rec0[108:112].
	binary.BigEndian.PutUint32(rec0[108:112], firstImageIndex)
	binary.BigEndian.PutUint32(rec0[128:132], 0x40)

	titleOffset := len(rec0) + len(exthBody)

	binary.BigEndian.PutUint32(rec0[84:88], uint32(titleOffset))
	binary.BigEndian.PutUint32(rec0[88:92], uint32(len(titleBytes)))

	rec0 = append(rec0, exthBody...)
	rec0 = append(rec0, titleBytes...)

	return rec0
}

type exthRec struct {
	typ  int
	data []byte
}

func buildEXTH(records []exthRec) []byte {
	var body []byte
	for _, r := range records {
		recLen := 8 + len(r.data)
		rec := make([]byte, 8)
		binary.BigEndian.PutUint32(rec[0:4], uint32(r.typ))
		binary.BigEndian.PutUint32(rec[4:8], uint32(recLen))
		rec = append(rec, r.data...)
		body = append(body, rec...)
	}

	header := make([]byte, 12)
	copy(header[0:4], "EXTH")
	binary.BigEndian.PutUint32(header[4:8], uint32(12+len(body)))
	binary.BigEndian.PutUint32(header[8:12], uint32(len(records)))

	return append(header, body...)
}

func uint32Bytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func generatePDF(dir string) {
	var b strings.Builder

	b.WriteString("%PDF-1.4\n")

	obj1Start := b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	obj2Start := b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	obj3Start := b.Len()
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]\n  /Resources << /XObject << /Im1 5 0 R >> >>\n>>\nendobj\n")

	obj4Start := b.Len()
	b.WriteString("4 0 obj\n<< /Title (Test PDF Book) /Author (Pat Draftson) /Subject (PDF test annotation.) >>\nendobj\n")

	obj5Start := b.Len()
	b.WriteString("5 0 obj\n<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length 3 >>\nstream\n\xff\xff\xff\nendstream\nendobj\n")

	xrefStart := b.Len()
	b.WriteString("xref\n")
	b.WriteString("0 6\n")
	b.WriteString(fmt.Sprintf("%010d 65535 f \n", 0))
	b.WriteString(fmt.Sprintf("%010d 00000 n \n", obj1Start))
	b.WriteString(fmt.Sprintf("%010d 00000 n \n", obj2Start))
	b.WriteString(fmt.Sprintf("%010d 00000 n \n", obj3Start))
	b.WriteString(fmt.Sprintf("%010d 00000 n \n", obj4Start))
	b.WriteString(fmt.Sprintf("%010d 00000 n \n", obj5Start))

	b.WriteString("trailer\n")
	b.WriteString("<< /Size 6 /Root 1 0 R /Info 4 0 R >>\n")
	b.WriteString("startxref\n")
	b.WriteString(fmt.Sprintf("%d\n", xrefStart))
	b.WriteString("%%EOF\n")

	must(os.WriteFile(filepath.Join(dir, "test.pdf"), []byte(b.String()), 0o644))
}

func generateFB2Win1251(dir string) {
	// UTF-8 source; we'll encode the whole thing to Windows-1251
	utfXML := `<?xml version="1.0" encoding="windows-1251"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>Проверка</book-title>
      <author>
        <first-name>Иван</first-name>
        <last-name>Тестов</last-name>
      </author>
      <lang>ru</lang>
    </title-info>
  </description>
</FictionBook>`

	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(utfXML))
	must(err)
	must(os.WriteFile(filepath.Join(dir, "test_win1251.fb2"), encoded, 0o644))
}

func generateFB2NoCover(dir string) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>No Cover Book</book-title>
      <author>
        <nickname>TestNick</nickname>
      </author>
      <lang>en</lang>
    </title-info>
  </description>
</FictionBook>`

	must(os.WriteFile(filepath.Join(dir, "test_nocover.fb2"), []byte(xml), 0o644))
}

func generateAZW3(dir string) {
	// A KF8 file with two image resources: a decoy (the first image record) and
	// the real cover right after it. firstImageIndex points at the decoy and
	// coverOffset=1 selects the cover, so locating it *requires* reading the
	// header offset correctly — a regressed offset would fall back to scanning
	// and wrongly return the decoy. The markers let the test tell them apart.
	decoy := []byte("\x89PNG\r\n\x1a\nDECOY" + strings.Repeat("\x00", 40))
	cover := []byte("\x89PNG\r\n\x1a\nCOVERIMG" + strings.Repeat("\x00", 40))

	rec0 := buildMOBIRecord0("Test AZW3 Book", "Kate Kindler", "An AZW3 description.", 65001, 1, 1)

	records := [][]byte{rec0, decoy, cover}
	numRecords := len(records)
	recTableSize := numRecords * 8

	header := make([]byte, 78)
	copy(header[0:32], padTo("Test AZW3", 32))
	binary.BigEndian.PutUint16(header[76:78], uint16(numRecords))

	recTable := make([]byte, recTableSize)
	offset := 78 + recTableSize
	for i, r := range records {
		binary.BigEndian.PutUint32(recTable[i*8:i*8+4], uint32(offset))
		offset += len(r)
	}

	var buf []byte
	buf = append(buf, header...)
	buf = append(buf, recTable...)
	for _, r := range records {
		buf = append(buf, r...)
	}

	must(os.WriteFile(filepath.Join(dir, "test.azw3"), buf, 0o644))
}

func fakePNG() string {
	return "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 50)
}

func padTo(s string, n int) []byte {
	b := make([]byte, n)
	copy(b, s)
	return b
}

func writeZipEntry(zw *zip.Writer, name, content string) {
	w, err := zw.Create(name)
	must(err)
	_, err = w.Write([]byte(content))
	must(err)
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
