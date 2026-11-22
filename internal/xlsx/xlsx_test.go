// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

func TestColName(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA", 16383: "XFD"} {
		if got := ColName(i); got != want {
			t.Errorf("ColName(%d) = %s, want %s", i, got, want)
		}
	}
}

func TestSerial(t *testing.T) {
	utc := time.UTC
	if got := Serial(time.Date(1900, 3, 1, 0, 0, 0, 0, utc), utc); got != 61 {
		t.Errorf("1900-03-01 = %v, want 61", got)
	}
	if got := Serial(time.Date(2026, 11, 22, 12, 0, 0, 0, utc), utc); got != 46348.5 {
		t.Errorf("2026-11-22 12:00 = %v, want 46348.5", got)
	}
	lis, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Skip(err)
	}
	// 12:00 UTC in July is 13:00 in Lisbon (WEST).
	got := Serial(time.Date(2026, 7, 1, 12, 0, 0, 0, utc), lis)
	want := Serial(time.Date(2026, 7, 1, 13, 0, 0, 0, utc), utc)
	if got != want {
		t.Errorf("wall clock in Lisbon: got %v, want %v", got, want)
	}
}

func TestSheetName(t *testing.T) {
	cases := map[string]string{
		"Summary":               "Summary",
		"a/b:c?d*e[f]g\\h":      "a b c d e f g h",
		"'quoted'":              "quoted",
		"":                      "Sheet",
		strings.Repeat("x", 40): strings.Repeat("x", 31),
	}
	for in, want := range cases {
		if got := SheetName(in); got != want {
			t.Errorf("SheetName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscape(t *testing.T) {
	if got := escape("a<b>&\"c\x00\x07\td￾"); got != "a&lt;b&gt;&amp;&quot;c\td" {
		t.Errorf("escape: %q", got)
	}
	if n := len([]rune(escape(strings.Repeat("é", maxCellText+10)))); n != maxCellText {
		t.Errorf("long text kept %d runes", n)
	}
}

// TestWorkbook writes a small workbook and checks that every part is
// well-formed XML and that values land where expected.
func TestWorkbook(t *testing.T) {
	var buf bytes.Buffer
	wb := New(&buf, time.UTC)
	s, err := wb.AddSheet("Data", []Column{{Header: "Name"}, {Header: "When", Style: DateTime}, {Header: "Value", Style: Decimal}},
		[]any{Title("Report")}, []any{Note("a note")}, []any{})
	if err != nil {
		t.Fatal(err)
	}
	v := 12.5
	var nilf *float64
	when := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	if err := s.Row("=cmd|' /C calc'!A0", when, &v); err != nil {
		t.Fatal(err)
	}
	if err := s.Row("<x & y>", time.Time{}, nilf); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.AddSheet("Data", nil, []any{"second sheet"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Row("late"); err == nil {
		t.Error("writing to a finished sheet should fail")
	}
	if err := wb.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed: %v", f.Name, err)
			}
		}
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels",
		"xl/styles.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml", "docProps/core.xml"} {
		if _, ok := parts[name]; !ok {
			t.Errorf("missing part %s", name)
		}
	}
	s1 := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`<pane ySplit="4" topLeftCell="A5"`,
		`<c r="A4" t="inlineStr" s="1"><is><t xml:space="preserve">Name</t>`,
		`<c r="A5" t="inlineStr"><is><t xml:space="preserve">=cmd|&#39; /C calc&#39;!A0</t>`,
		`<c r="B5" s="2"><v>46348.5</v></c>`,
		`<c r="C5" s="4"><v>12.5</v></c>`,
		`&lt;x &amp; y&gt;`,
		`<autoFilter ref="A4:C6"/>`,
	} {
		if strings.Contains(want, "&#39;") {
			want = strings.ReplaceAll(want, "&#39;", "'")
		}
		if !strings.Contains(s1, want) {
			t.Errorf("sheet1 lacks %s", want)
		}
	}
	if strings.Contains(s1, `r="B6"`) || strings.Contains(s1, `r="C6"`) {
		t.Error("empty values should leave the cells out")
	}
	if !strings.Contains(parts["xl/workbook.xml"], `<sheet name="Data (2)" sheetId="2"`) {
		t.Error("duplicate sheet names should be made unique")
	}
	if !strings.Contains(parts["xl/workbook.xml"], `'Data'!$A$4:$C$6`) {
		t.Error("filter database name missing")
	}
}
