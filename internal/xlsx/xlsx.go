// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

// Package xlsx writes Office Open XML spreadsheets (.xlsx) that Excel,
// LibreOffice and Google Sheets open. It streams: rows go straight into the
// zip archive, so large sheets need little memory. Sheets are written one
// after the other; adding a sheet finishes the previous one.
package xlsx

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxRows is the number of rows a worksheet can hold in Excel.
const MaxRows = 1048576

// maxCellText is the longest text Excel keeps in one cell.
const maxCellText = 32767

// Style is the number format of a column's values.
type Style int

const (
	General  Style = iota
	Integer        // 1234
	Decimal        // 1234.56
	Percent3       // 99.987 (a percentage stored as 0..100, three decimals)
	DateTime       // 2026-11-22 14:05:09
	Date           // 2026-11-22
	Text           // left as typed
)

// cellXfs indexes (see stylesXML).
const (
	xfDefault = iota
	xfHeader
	xfDateTime
	xfDate
	xfDecimal
	xfPercent3
	xfInteger
	xfTitle
	xfMuted
)

func (s Style) xf() int {
	switch s {
	case Integer:
		return xfInteger
	case Decimal:
		return xfDecimal
	case Percent3:
		return xfPercent3
	case DateTime:
		return xfDateTime
	case Date:
		return xfDate
	}
	return xfDefault
}

// Column describes one column of a table sheet.
type Column struct {
	Header string
	// Width in characters; 0 picks a width from the header and style.
	Width float64
	Style Style
}

// Title is a bold line in the preamble above a sheet's table.
type Title string

// Note is a grey line in the preamble above a sheet's table.
type Note string

// Workbook is an .xlsx file being written.
type Workbook struct {
	zw     *zip.Writer
	loc    *time.Location
	sheets []*Sheet
	cur    *Sheet
	closed bool
	// Creator is stored in the document properties.
	Creator string
}

// New starts a workbook written to w. Times are written as wall-clock times
// in loc (spreadsheets have no time zones); nil means time.Local.
func New(w io.Writer, loc *time.Location) *Workbook {
	if loc == nil {
		loc = time.Local
	}
	return &Workbook{zw: zip.NewWriter(w), loc: loc}
}

// Sheet is a worksheet being written.
type Sheet struct {
	wb        *Workbook
	name      string
	cols      []Column
	bw        *bufio.Writer
	row       int // rows written so far
	headerRow int // 1-based row of the column headers, 0 without a table
	err       error
}

// Rows is the number of rows written so far, preamble and header included.
func (s *Sheet) Rows() int { return s.row }

// Name is the sheet's name as stored.
func (s *Sheet) Name() string { return s.name }

// AddSheet finishes the current sheet and starts a new one. The preamble rows
// (Title, Note, or any cell values) come first, then a header row with the
// column names, frozen and with filter buttons. With no columns there is no
// table, only the preamble.
func (wb *Workbook) AddSheet(name string, cols []Column, preamble ...[]any) (*Sheet, error) {
	if wb.closed {
		return nil, errors.New("xlsx: workbook is closed")
	}
	if err := wb.finishSheet(); err != nil {
		return nil, err
	}
	name = wb.uniqueName(SheetName(name))
	idx := len(wb.sheets) + 1
	f, err := wb.zw.Create(fmt.Sprintf("xl/worksheets/sheet%d.xml", idx))
	if err != nil {
		return nil, err
	}
	s := &Sheet{wb: wb, name: name, cols: cols, bw: bufio.NewWriterSize(f, 64<<10)}
	wb.sheets = append(wb.sheets, s)
	wb.cur = s

	s.raw(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<sheetPr><pageSetUpPr fitToPage="1"/></sheetPr>`)
	if len(cols) > 0 {
		s.headerRow = len(preamble) + 1
		s.raw(fmt.Sprintf(`<sheetViews><sheetView workbookViewId="0"%s><pane ySplit="%d" topLeftCell="A%d" activePane="bottomLeft" state="frozen"/><selection pane="bottomLeft" activeCell="A%d" sqref="A%d"/></sheetView></sheetViews>`,
			map[bool]string{true: ` tabSelected="1"`}[idx == 1], s.headerRow, s.headerRow+1, s.headerRow+1, s.headerRow+1))
		s.raw(`<cols>`)
		for i, c := range cols {
			s.raw(fmt.Sprintf(`<col min="%d" max="%d" width="%s" customWidth="1"/>`, i+1, i+1,
				strconv.FormatFloat(c.width(), 'f', 1, 64)))
		}
		s.raw(`</cols>`)
	} else {
		s.raw(`<sheetViews><sheetView workbookViewId="0"` + map[bool]string{true: ` tabSelected="1"`}[idx == 1] + `/></sheetViews><cols><col min="1" max="1" width="28" customWidth="1"/><col min="2" max="2" width="60" customWidth="1"/></cols>`)
	}
	s.raw(`<sheetData>`)
	for _, p := range preamble {
		s.writeRow(p, true)
	}
	if len(cols) > 0 {
		s.row++
		s.raw(fmt.Sprintf(`<row r="%d">`, s.row))
		for i, c := range cols {
			s.cell(i, c.Header, xfHeader)
		}
		s.raw(`</row>`)
	}
	return s, s.err
}

func (c Column) width() float64 {
	if c.Width > 0 {
		return c.Width
	}
	w := float64(utf8.RuneCountInString(c.Header)) + 4
	min := map[Style]float64{DateTime: 20, Date: 12, Decimal: 11, Percent3: 11, Integer: 9}[c.Style]
	if w < min {
		w = min
	}
	if w < 8 {
		w = 8
	}
	return w
}

// Row appends a row. Values may be string, fmt.Stringer, bool, any integer or
// float type, pointers to those (nil leaves the cell empty), time.Time and
// *time.Time. A zero time leaves the cell empty.
func (s *Sheet) Row(vals ...any) error {
	if s.err != nil {
		return s.err
	}
	if s.wb.cur != s {
		return errors.New("xlsx: the sheet is already finished")
	}
	if s.row >= MaxRows {
		return ErrSheetFull
	}
	s.writeRow(vals, false)
	return s.err
}

// Full reports whether the sheet cannot take another row.
func (s *Sheet) Full() bool { return s.row >= MaxRows }

// ErrSheetFull is returned by Row when the sheet already has MaxRows rows.
var ErrSheetFull = errors.New("xlsx: the sheet is full")

func (s *Sheet) writeRow(vals []any, preamble bool) {
	s.row++
	s.raw(fmt.Sprintf(`<row r="%d">`, s.row))
	for i, v := range vals {
		xf := xfDefault
		if preamble {
			switch t := v.(type) {
			case Title:
				v, xf = string(t), xfTitle
			case Note:
				v, xf = string(t), xfMuted
			}
		} else if i < len(s.cols) {
			xf = s.cols[i].Style.xf()
		}
		s.cell(i, v, xf)
	}
	s.raw(`</row>`)
}

func (s *Sheet) cell(col int, v any, xf int) {
	ref := ColName(col) + strconv.Itoa(s.row)
	num := func(f float64) {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return
		}
		s.raw(fmt.Sprintf(`<c r="%s"%s><v>%s</v></c>`, ref, styleAttr(xf), strconv.FormatFloat(f, 'g', -1, 64)))
	}
	switch t := v.(type) {
	case nil:
	case string:
		s.text(ref, t, xf)
	case Title:
		s.text(ref, string(t), xfTitle)
	case Note:
		s.text(ref, string(t), xfMuted)
	case bool:
		b := "0"
		if t {
			b = "1"
		}
		s.raw(fmt.Sprintf(`<c r="%s" t="b"%s><v>%s</v></c>`, ref, styleAttr(xf), b))
	case int:
		num(float64(t))
	case int16:
		num(float64(t))
	case int32:
		num(float64(t))
	case int64:
		num(float64(t))
	case float32:
		num(float64(t))
	case float64:
		num(t)
	case *int:
		if t != nil {
			num(float64(*t))
		}
	case *int64:
		if t != nil {
			num(float64(*t))
		}
	case *float64:
		if t != nil {
			num(*t)
		}
	case time.Time:
		s.time(ref, t, xf)
	case *time.Time:
		if t != nil {
			s.time(ref, *t, xf)
		}
	case fmt.Stringer:
		s.text(ref, t.String(), xf)
	default:
		s.text(ref, fmt.Sprint(t), xf)
	}
}

func (s *Sheet) time(ref string, t time.Time, xf int) {
	if t.IsZero() {
		return
	}
	if xf != xfDate && xf != xfDateTime {
		xf = xfDateTime
	}
	s.raw(fmt.Sprintf(`<c r="%s"%s><v>%s</v></c>`, ref, styleAttr(xf),
		strconv.FormatFloat(Serial(t, s.wb.loc), 'f', -1, 64)))
}

func (s *Sheet) text(ref, v string, xf int) {
	if v == "" {
		return
	}
	s.raw(fmt.Sprintf(`<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">`, ref, styleAttr(xf)))
	s.raw(escape(v))
	s.raw(`</t></is></c>`)
}

func styleAttr(xf int) string {
	if xf == xfDefault {
		return ""
	}
	return ` s="` + strconv.Itoa(xf) + `"`
}

func (s *Sheet) raw(v string) {
	if s.err == nil {
		_, s.err = s.bw.WriteString(v)
	}
}

func (wb *Workbook) finishSheet() error {
	s := wb.cur
	if s == nil {
		return nil
	}
	wb.cur = nil
	s.raw(`</sheetData>`)
	if s.headerRow > 0 && s.row >= s.headerRow {
		s.raw(fmt.Sprintf(`<autoFilter ref="%s"/>`, s.filterRef()))
	}
	s.raw(`<pageMargins left="0.5" right="0.5" top="0.75" bottom="0.75" header="0.3" footer="0.3"/><pageSetup orientation="landscape" fitToWidth="1" fitToHeight="0"/></worksheet>`)
	if s.err == nil {
		s.err = s.bw.Flush()
	}
	return s.err
}

func (s *Sheet) filterRef() string {
	return fmt.Sprintf("A%d:%s%d", s.headerRow, ColName(len(s.cols)-1), s.row)
}

// Close finishes the last sheet and writes the rest of the package. It does
// not close the underlying writer.
func (wb *Workbook) Close() error {
	if wb.closed {
		return nil
	}
	if len(wb.sheets) == 0 {
		if _, err := wb.AddSheet("Sheet1", nil); err != nil {
			return err
		}
	}
	if err := wb.finishSheet(); err != nil {
		return err
	}
	wb.closed = true
	files := []struct{ name, body string }{
		{"[Content_Types].xml", wb.contentTypes()},
		{"_rels/.rels", rootRels},
		{"docProps/app.xml", appXML},
		{"docProps/core.xml", wb.coreXML()},
		{"xl/workbook.xml", wb.workbookXML()},
		{"xl/_rels/workbook.xml.rels", wb.workbookRels()},
		{"xl/styles.xml", stylesXML},
	}
	for _, f := range files {
		w, err := wb.zw.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			return err
		}
	}
	return wb.zw.Close()
}

// ---------------------------------------------------------------- helpers

// excelEpoch is day 0 of the 1900 date system as Excel counts it (Excel
// treats 1900 as a leap year, which this offset absorbs for dates after
// 1 March 1900).
var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// Serial converts t to an Excel date serial number (days since 1899-12-30,
// with the time of day as a fraction), using t's wall-clock time in loc.
// Precision is one millisecond.
func Serial(t time.Time, loc *time.Location) float64 {
	t = t.In(loc)
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	ms := wall.Sub(excelEpoch).Milliseconds()
	return float64(ms) / 86400000
}

// ColName returns the column letters of a 0-based column index (0 → A, 26 → AA).
func ColName(i int) string {
	var b []byte
	for i++; i > 0; i = (i - 1) / 26 {
		b = append([]byte{byte('A' + (i-1)%26)}, b...)
	}
	return string(b)
}

// SheetName makes s a valid worksheet name: at most 31 characters and none
// of : \ / ? * [ ]. It may not start or end with an apostrophe.
func SheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), "'")
	if r := []rune(s); len(r) > 31 {
		s = strings.TrimSpace(string(r[:31]))
	}
	if s == "" {
		s = "Sheet"
	}
	return s
}

func (wb *Workbook) uniqueName(name string) string {
	taken := func(n string) bool {
		for _, s := range wb.sheets {
			if strings.EqualFold(s.name, n) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		base := []rune(name)
		if len(base)+len(suffix) > 31 {
			base = base[:31-len(suffix)]
		}
		if n := string(base) + suffix; !taken(n) {
			return n
		}
	}
}

// escape returns v as XML text, without characters XML 1.0 cannot hold and
// cut to the length Excel keeps in a cell.
func escape(v string) string {
	if utf8.RuneCountInString(v) > maxCellText {
		v = string([]rune(v)[:maxCellText])
	}
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r == utf8.RuneError, r >= 0xD800 && r <= 0xDFFF:
			// not allowed in XML 1.0: drop it
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func xmlAttr(v string) string { return escape(v) }

func (wb *Workbook) contentTypes() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for i := range wb.sheets {
		fmt.Fprintf(&b, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func (wb *Workbook) workbookXML() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<workbookPr/><bookViews><workbookView activeTab="0"/></bookViews><sheets>`)
	for i, s := range wb.sheets {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(s.name), i+1, i+1)
	}
	b.WriteString(`</sheets>`)
	var names strings.Builder
	for i, s := range wb.sheets {
		if s.headerRow > 0 && s.row >= s.headerRow {
			ref := s.filterRef()
			parts := strings.SplitN(ref, ":", 2)
			abs := func(cell string) string {
				j := strings.IndexAny(cell, "0123456789")
				return "$" + cell[:j] + "$" + cell[j:]
			}
			fmt.Fprintf(&names, `<definedName name="_xlnm._FilterDatabase" localSheetId="%d" hidden="1">'%s'!%s:%s</definedName>`,
				i, xmlAttr(strings.ReplaceAll(s.name, "'", "''")), abs(parts[0]), abs(parts[1]))
		}
	}
	if names.Len() > 0 {
		b.WriteString(`<definedNames>` + names.String() + `</definedNames>`)
	}
	b.WriteString(`</workbook>`)
	return b.String()
}

func (wb *Workbook) workbookRels() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range wb.sheets {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, len(wb.sheets)+1)
	b.WriteString(`</Relationships>`)
	return b.String()
}

func (wb *Workbook) coreXML() string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	creator := wb.Creator
	if creator == "" {
		creator = "MonitorIQ"
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:creator>` + escape(creator) + `</dc:creator>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified>` +
		`</cp:coreProperties>`
}

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
	`</Relationships>`

const appXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>MonitorIQ</Application></Properties>`

// stylesXML defines the cell formats; the cellXfs order matches the xf* constants.
const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<numFmts count="3">` +
	`<numFmt numFmtId="164" formatCode="yyyy\-mm\-dd\ hh:mm:ss"/>` +
	`<numFmt numFmtId="165" formatCode="yyyy\-mm\-dd"/>` +
	`<numFmt numFmtId="166" formatCode="0.000"/>` +
	`</numFmts>` +
	`<fonts count="4">` +
	`<font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="14"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><sz val="10"/><color rgb="FF6B7280"/><name val="Calibri"/><family val="2"/></font>` +
	`</fonts>` +
	`<fills count="3">` +
	`<fill><patternFill patternType="none"/></fill>` +
	`<fill><patternFill patternType="gray125"/></fill>` +
	`<fill><patternFill patternType="solid"><fgColor rgb="FF3B4A5E"/><bgColor indexed="64"/></patternFill></fill>` +
	`</fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="9">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>` +
	`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="2" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="166" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="1" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="0" fontId="2" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`<xf numFmtId="0" fontId="3" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`
