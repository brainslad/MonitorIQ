// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"strings"
	"testing"
)

func TestNormalizePushoverKeys(t *testing.T) {
	const a, b = "uQiRzpo4DXghDmr9QzzfQu27cmVRsG", "gznej3rKEVAvPUxu9vvNnqpmZpokzF"
	got, err := NormalizePushoverKeys(" " + a + ";\n" + b + " , " + a)
	if err != nil || got != a+", "+b {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := NormalizePushoverKeys(""); err != nil || got != "" {
		t.Fatalf("empty: %q, %v", got, err)
	}
	_, err = NormalizePushoverKeys(a + ", tooshortkey123")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), a) {
		t.Error("the error repeats a full key")
	}
	if MaskKey(a) != "uQiR…RsG" {
		t.Errorf("mask %q", MaskKey(a))
	}
}
