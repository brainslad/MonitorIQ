// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"strings"
	"testing"
)

func TestGreeting(t *testing.T) {
	cases := []struct {
		u    User
		want string
	}{
		{User{Username: "jose.silva"}, "Jose"},
		{User{Username: "admin"}, "Admin"},
		{User{Username: "jdoe@example.com"}, "Jdoe"},
		{User{Username: "jsilva", DisplayName: "joão Silva"}, "João"}, // from the directory
		{User{Username: "jsilva", DisplayName: "João Silva", CustomName: true}, "João Silva"},
		{User{Username: "x", DisplayName: "  ", CustomName: true}, "X"},
	}
	for _, c := range cases {
		if got := c.u.Greeting(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.u, got, c.want)
		}
	}
}

func TestCleanDisplayName(t *testing.T) {
	if v, err := CleanDisplayName("  Maria   da  Costa "); err != nil || v != "Maria da Costa" {
		t.Errorf("got %q %v", v, err)
	}
	if _, err := CleanDisplayName(strings.Repeat("a", MaxDisplayName+1)); err == nil {
		t.Error("long name accepted")
	}
	if _, err := CleanDisplayName("a\u0007b"); err == nil {
		t.Error("control character accepted")
	}
}

func TestWeakPassword(t *testing.T) {
	for _, pw := range []string{"12345678", "87654321", "abcdefgh", "aaaaaaaa", "Password1", "ADMIN12345", "qwertyuiop", "11111111"} {
		if !weakPassword(pw) {
			t.Errorf("weakPassword(%q) = false, want true", pw)
		}
		if _, err := hashPassword(pw); err == nil {
			t.Errorf("hashPassword(%q) accepted a weak password", pw)
		}
	}
	for _, pw := range []string{"correct horse battery", "Tr0ub4dor&3x", "9fK2mQ7xLp", "s2mon03-uptime!"} {
		if weakPassword(pw) {
			t.Errorf("weakPassword(%q) = true, want false", pw)
		}
	}
	if _, err := hashPassword(strings.Repeat("x7", 40)); err == nil {
		t.Error("hashPassword accepted an 80-byte password (bcrypt only uses 72)")
	}
}
