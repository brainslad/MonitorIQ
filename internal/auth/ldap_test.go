// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package auth

import (
	"os"
	"strings"
	"testing"
)

// TestLDAPIntegration runs against a real directory when MIQ_LDAP_URL is set,
// for example with the osixia/openldap image seeded as in the README:
//
//	MIQ_LDAP_URL=ldap://127.0.0.1:389 go test ./internal/auth -run LDAP
func TestLDAPIntegration(t *testing.T) {
	u := os.Getenv("MIQ_LDAP_URL")
	if u == "" {
		t.Skip("MIQ_LDAP_URL not set")
	}
	c := LDAPConfig{
		Enabled: true, URL: u, BindDN: "cn=admin,dc=example,dc=org", BindPassword: "adminpw",
		BaseDN: "ou=people,dc=example,dc=org", UserFilter: OpenLDAPUserFilter,
		UsernameAttr: "uid", NameAttr: "cn", EmailAttr: "mail",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	id, err := c.Authenticate("jdoe", "JanePass123")
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jdoe" || id.DisplayName != "Jane Doe" || id.Email != "jane.doe@example.org" ||
		id.ExternalID != "uid=jdoe,ou=people,dc=example,dc=org" {
		t.Fatalf("%+v", id)
	}
	if _, err := c.Authenticate("jdoe", "wrong"); err != ErrBadCredentials {
		t.Fatal("wrong password:", err)
	}
	if _, err := c.Authenticate("nobody", "x"); err != ErrBadCredentials {
		t.Fatal("unknown user:", err)
	}
	if _, err := c.Authenticate("jdoe)(uid=*", "JanePass123"); err != ErrBadCredentials {
		t.Fatal("filter injection:", err)
	}
	c.RequiredGroup = "cn=monitoriq-admins,ou=groups,dc=example,dc=org"
	if _, err := c.Authenticate("jdoe", "JanePass123"); err != nil {
		t.Fatal("member denied:", err)
	}
	if _, err := c.Authenticate("bob", "BobPass123"); err != ErrDenied {
		t.Fatal("non-member allowed:", err)
	}
	msg, err := c.Test("bob")
	if err != nil || !strings.Contains(msg, "NOT a member") {
		t.Fatal(msg, err)
	}
	c.BindPassword = "bad"
	if _, err := c.Test(""); err == nil || !strings.Contains(err.Error(), "service account") {
		t.Fatal("bad bind password not reported:", err)
	}
}
