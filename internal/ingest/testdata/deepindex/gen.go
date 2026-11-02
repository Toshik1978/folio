//go:build ignore

// gen.go regenerates the deep-index INPX fixtures. Run from this directory:
//
//	go run gen.go
//
// Output is byte-identical across runs (fixed ZIP mtimes, deterministic filler).
//
// Fixture layout (library.inpx sits next to both archives):
//
//	library.inpx
//	  a.fb2-000001-000004.inp  -> archive a.fb2-000001-000004.zip
//	  b.fb2-000005-000006.inp  -> archive b.fb2-000005-000006.zip
//	  c.fb2-000007-000010.inp  -> archive c.fb2-000007-000010.zip
//
// Books (id = INP FILE/LIBID field; entry = ZIP entry name):
//
//	id 1  entry 1.fb2  title "Книга с аннотацией UTF-8"
//	      UTF-8 annotation; expected text "Уникальнаяаннотация Звездолёт Бригантина".
//	id 2  entry 2.fb2  title "Книга в кодировке 1251"
//	      Real windows-1251 bytes, encoding="windows-1251" in the XML declaration;
//	      expected annotation "Кириллица Самовар Лукоморье".
//	id 3  entry 3.fb2  title "Книга без аннотации"
//	      Valid FB2, no <annotation>; expected annotation empty.
//	id 4  entry 4.fb2  title "Повреждённая книга"
//	      Malformed XML before </description> (mismatched tag); header parse must error.
//	id 5  entry 5.fb2  title "Книга с большой бинарной секцией"
//	      Valid annotation "Громадный Бинарник Мельница"; followed by a ~270 KB
//	      <binary> after </description>, so a header-only parse reads far less
//	      than the whole entry.
//	id 6  entry 6.pdf  title "Книга только в PDF"
//	      PDF-only book (ext pdf); never parsed for an annotation.
//
// Archive c.fb2-000007-000010.zip (non-FB2 formats and large/broken entries):
//
//	id 7  entry 7.epub  title "Книга в формате EPUB"
//	      Valid EPUB; dc:description "Эпубное описание Маяк Верста".
//	id 8  entry 8.mobi  title "Книга в формате MOBI"
//	      Valid MOBI; EXTH description "Мобийное описание Дирижабль Заря".
//	id 9  entry 9.epub  title "Усечённая книга EPUB"
//	      EPUB zip cut before its central directory; unreadable.
//	id 10 entry 10.fb2  title "Книга с огромной бинарной секцией"
//	      <description> with annotation "Исполинский Бинарник Маяк" at the start,
//	      followed by a >1 MiB <binary> (incompressible base64), so reaching it
//	      requires inflating far beyond 64 KiB.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

const (
	archiveA = "a.fb2-000001-000004"
	archiveB = "b.fb2-000005-000006"
	archiveC = "c.fb2-000007-000010"
)

var fixedTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	writeZip(filepath.Join(dir, archiveA+".zip"), []entry{
		{"1.fb2", []byte(fb2("utf-8", "Книга с аннотацией UTF-8",
			"<annotation><p>Уникальнаяаннотация Звездолёт Бригантина</p></annotation>", ""))},
		{"2.fb2", encode1251(fb2("windows-1251", "Книга в кодировке 1251",
			"<annotation><p>Кириллица Самовар Лукоморье</p></annotation>", ""))},
		{"3.fb2", []byte(fb2("utf-8", "Книга без аннотации", "", ""))},
		{"4.fb2", []byte(strings.Replace(
			fb2("utf-8", "Повреждённая книга", "<annotation><p>Сломано</p></annotation>", ""),
			"</book-title>", "</booktitle>", 1))},
	})
	writeZip(filepath.Join(dir, archiveB+".zip"), []entry{
		{"5.fb2", []byte(fb2("utf-8", "Книга с большой бинарной секцией",
			"<annotation><p>Громадный Бинарник Мельница</p></annotation>", bigBinary()))},
		{"6.pdf", []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")},
	})
	validEPUB := buildEPUB("Книга в формате EPUB", "Эпубное описание Маяк Верста")
	writeZip(filepath.Join(dir, archiveC+".zip"), []entry{
		{"7.epub", validEPUB},
		{"8.mobi", buildMOBI("Книга в формате MOBI", "Мобийное описание Дирижабль Заря")},
		{"9.epub", truncateBeforeCentralDirectory(buildEPUB("Усечённая книга EPUB", "Не прочитать"))},
		{"10.fb2", []byte(fb2("utf-8", "Книга с огромной бинарной секцией",
			"<annotation><p>Исполинский Бинарник Маяк</p></annotation>", hugeBinary()))},
	})

	writeZip(filepath.Join(dir, "library.inpx"), []entry{
		{archiveA + ".inp", []byte(
			inp("Авторов,Иван,:", "Книга с аннотацией UTF-8", "1", "fb2") +
				inp("Авторов,Пётр,:", "Книга в кодировке 1251", "2", "fb2") +
				inp("Авторов,Сергей,:", "Книга без аннотации", "3", "fb2") +
				inp("Авторов,Олег,:", "Повреждённая книга", "4", "fb2"))},
		{archiveB + ".inp", []byte(
			inp("Авторов,Глеб,:", "Книга с большой бинарной секцией", "5", "fb2") +
				inp("Авторов,Лев,:", "Книга только в PDF", "6", "pdf"))},
		{archiveC + ".inp", []byte(
			inp("Авторов,Мирон,:", "Книга в формате EPUB", "7", "epub") +
				inp("Авторов,Ян,:", "Книга в формате MOBI", "8", "mobi") +
				inp("Авторов,Назар,:", "Усечённая книга EPUB", "9", "epub") +
				inp("Авторов,Фёдор,:", "Книга с огромной бинарной секцией", "10", "fb2"))},
	})
	fmt.Println("deepindex fixtures generated")
}

type entry struct {
	name string
	data []byte
}

// inp builds one 15-field INP record in the layout the INPX reader expects.
func inp(author, title, id, ext string) string {
	fields := []string{
		author, "sf:", title, "", "", id, "1000", id, "0", ext,
		"2008-07-05", "ru", "3", "",
	}

	return strings.Join(fields, "\x04") + "\x04\r\n"
}

func fb2(encoding, title, annotation, tail string) string {
	return `<?xml version="1.0" encoding="` + encoding + `"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
<description><title-info><genre>sf</genre>
<author><first-name>Иван</first-name><last-name>Авторов</last-name></author>
<book-title>` + title + `</book-title>` + annotation + `<lang>ru</lang></title-info></description>
<body><section><p>Текст.</p></section></body>` + tail + `
</FictionBook>
`
}

// bigBinary returns a <binary> element of ~270 KB of deterministic base64.
func bigBinary() string {
	raw := make([]byte, 200*1024)
	x := uint32(12345)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}

	return `<binary id="cover.jpg" content-type="image/jpeg">` + base64.StdEncoding.EncodeToString(raw) + `</binary>`
}

// hugeBinary returns a <binary> element of ~1.4 MiB of deterministic base64.
// The source bytes come from a fixed-seed LCG, so they barely compress and the
// entry stays larger than 1 MiB even once deflated.
func hugeBinary() string {
	raw := make([]byte, 1100*1024)
	x := uint32(54321)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}

	return `<binary id="cover.jpg" content-type="image/jpeg">` + base64.StdEncoding.EncodeToString(raw) + `</binary>`
}

// buildEPUB returns a minimal valid EPUB with a title and dc:description.
func buildEPUB(title, description string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fixedTime})
		must(err)
		_, err = w.Write([]byte(content))
		must(err)
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)
	add("OEBPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>`+title+`</dc:title>
    <dc:creator>Иван Авторов</dc:creator>
    <dc:description>`+description+`</dc:description>
    <dc:language>ru</dc:language>
  </metadata>
  <manifest/>
</package>`)
	must(zw.Close())

	return buf.Bytes()
}

// truncateBeforeCentralDirectory cuts a zip at its first central-directory
// header, leaving the local entries but no way to list them.
func truncateBeforeCentralDirectory(zipData []byte) []byte {
	i := bytes.Index(zipData, []byte("PK\x01\x02"))
	if i < 0 {
		panic("no central directory found")
	}

	return zipData[:i]
}

// buildMOBI returns a minimal MOBI (PalmDB header, one record) with an EXTH
// author and description.
func buildMOBI(title, description string) []byte {
	be := binary.BigEndian
	exth := func(typ uint32, data string) []byte {
		rec := make([]byte, 8, 8+len(data))
		be.PutUint32(rec[0:4], typ)
		be.PutUint32(rec[4:8], uint32(8+len(data)))

		return append(rec, data...)
	}
	body := append(exth(100, "Иван Авторов"), exth(103, description)...)
	exthBlock := make([]byte, 12, 12+len(body))
	copy(exthBlock[0:4], "EXTH")
	be.PutUint32(exthBlock[4:8], uint32(12+len(body)))
	be.PutUint32(exthBlock[8:12], 2)
	exthBlock = append(exthBlock, body...)

	const mobiHeaderLen = 116
	rec0 := make([]byte, 16+mobiHeaderLen)
	copy(rec0[16:20], "MOBI")
	be.PutUint32(rec0[20:24], mobiHeaderLen)
	be.PutUint32(rec0[28:32], 65001)
	be.PutUint32(rec0[108:112], 0xFFFFFFFF) // no images
	be.PutUint32(rec0[128:132], 0x40)       // EXTH present
	be.PutUint32(rec0[84:88], uint32(len(rec0)+len(exthBlock)))
	be.PutUint32(rec0[88:92], uint32(len(title)))
	rec0 = append(rec0, exthBlock...)
	rec0 = append(rec0, title...)

	header := make([]byte, 78)
	copy(header[0:32], "Deep Index MOBI")
	be.PutUint16(header[76:78], 1)
	recTable := make([]byte, 8)
	be.PutUint32(recTable[0:4], uint32(78+len(recTable)))

	out := append(header, recTable...)

	return append(out, rec0...)
}

func encode1251(s string) []byte {
	b, err := charmap.Windows1251.NewEncoder().Bytes([]byte(s))
	must(err)

	return b
}

func writeZip(path string, entries []entry) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: fixedTime})
		must(err)
		_, err = w.Write(e.data)
		must(err)
	}
	must(zw.Close())
	must(os.WriteFile(path, buf.Bytes(), 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
