// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"strings"
	"testing"
)

func TestValidateGroup(t *testing.T) {
	g := Group{Name: "  Web   shop ", Description: " Front end \n"}
	if err := ValidateGroup(&g); err != nil {
		t.Fatal(err)
	}
	if g.Name != "Web shop" || g.Description != "Front end" || g.Color != GroupColors[0] {
		t.Fatalf("not normalised: %+v", g)
	}
	bad := []Group{
		{Name: "   "},
		{Name: strings.Repeat("x", 81)},
		{Name: "ok", Description: strings.Repeat("y", 501)},
		{Name: "ok", Color: "pink"},
	}
	for i, b := range bad {
		if err := ValidateGroup(&b); err == nil {
			t.Errorf("case %d should be rejected: %+v", i, b)
		}
	}
}

func TestGroupStatus(t *testing.T) {
	cases := []struct {
		g    Group
		want string
	}{
		{Group{Monitors: 3, Up: 2, Down: 1}, "down"},
		{Group{Monitors: 2, Up: 1, Degraded: 1}, "degraded"},
		{Group{Monitors: 2, Up: 1, Paused: 1}, "up"},
		{Group{Monitors: 1, Paused: 1}, "unknown"},
		{Group{}, "unknown"},
	}
	for _, c := range cases {
		if got := c.g.Status(); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.g, got, c.want)
		}
	}
	if (Group{Monitors: 4, Paused: 1}).Active() != 3 {
		t.Error("Active")
	}
}
