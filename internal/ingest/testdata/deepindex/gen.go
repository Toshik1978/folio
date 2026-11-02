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
package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
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

	writeZip(filepath.Join(dir, "library.inpx"), []entry{
		{archiveA + ".inp", []byte(
			inp("Авторов,Иван,:", "Книга с аннотацией UTF-8", "1", "fb2") +
				inp("Авторов,Пётр,:", "Книга в кодировке 1251", "2", "fb2") +
				inp("Авторов,Сергей,:", "Книга без аннотации", "3", "fb2") +
				inp("Авторов,Олег,:", "Повреждённая книга", "4", "fb2"))},
		{archiveB + ".inp", []byte(
			inp("Авторов,Глеб,:", "Книга с большой бинарной секцией", "5", "fb2") +
				inp("Авторов,Лев,:", "Книга только в PDF", "6", "pdf"))},
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
