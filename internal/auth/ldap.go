package auth

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Default user filters. {username} is replaced by the escaped login name.
const (
	ADUserFilter       = "(&(objectClass=user)(objectCategory=person)(sAMAccountName={username}))"
	OpenLDAPUserFilter = "(&(objectClass=inetOrgPerson)(uid={username}))"
)

// adMatchingRuleInChain makes memberOf checks follow nested groups in
// Active Directory.
const adMatchingRuleInChain = "1.2.840.113556.1.4.1941"

// LDAPConfig configures sign-in against LDAP or Active Directory.
type LDAPConfig struct {
	Enabled            bool   `json:"enabled"`
	URL                string `json:"url"` // ldap://host:389 or ldaps://host:636
	StartTLS           bool   `json:"start_tls"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	BindDN             string `json:"bind_dn"` // service account; empty = anonymous search
	BindPassword       string `json:"bind_password"`
	BaseDN             string `json:"base_dn"`
	UserFilter         string `json:"user_filter"`
	UsernameAttr       string `json:"username_attr"`
	NameAttr           string `json:"name_attr"`
	EmailAttr          string `json:"email_attr"`
	RequiredGroup      string `json:"required_group"` // group DN; empty = any user found by the filter
	NestedGroups       bool   `json:"nested_groups"`  // Active Directory only
	AutoCreate         bool   `json:"auto_create"`
}

func (c *LDAPConfig) Validate() error {
	if !validURL(c.URL, "ldap", "ldaps") {
		return errors.New("the server URL must look like ldaps://dc01.example.com:636 or ldap://host:389")
	}
	if strings.TrimSpace(c.BaseDN) == "" {
		return errors.New("the base DN is required")
	}
	if !strings.Contains(c.UserFilter, "{username}") {
		return errors.New("the user filter must contain {username}")
	}
	if _, err := ldap.CompileFilter(strings.ReplaceAll(c.UserFilter, "{username}", "x")); err != nil {
		return fmt.Errorf("invalid user filter: %v", err)
	}
	if c.RequiredGroup != "" {
		if _, err := ldap.ParseDN(c.RequiredGroup); err != nil {
			return fmt.Errorf("invalid group DN: %v", err)
		}
	}
	return nil
}

const ldapTimeout = 10 * time.Second

func (c *LDAPConfig) dial() (*ldap.Conn, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: c.InsecureSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in, for test labs
	conn, err := ldap.DialURL(c.URL, ldap.DialWithDialer(&net.Dialer{Timeout: ldapTimeout}), ldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s: %w", u.Host, err)
	}
	conn.SetTimeout(ldapTimeout)
	if c.StartTLS && u.Scheme == "ldap" {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("StartTLS failed: %w", err)
		}
	}
	return conn, nil
}

func (c *LDAPConfig) serviceBind(conn *ldap.Conn) error {
	var err error
	if c.BindDN == "" {
		err = conn.UnauthenticatedBind("")
	} else {
		err = conn.Bind(c.BindDN, c.BindPassword)
	}
	if err != nil {
		return fmt.Errorf("service account bind failed: %w", err)
	}
	return nil
}

func (c *LDAPConfig) attrs() []string {
	a := []string{"dn"}
	for _, x := range []string{c.UsernameAttr, c.NameAttr, c.EmailAttr} {
		if x != "" {
			a = append(a, x)
		}
	}
	return a
}

// errNoUser is returned when the filter matches no entry.
var errNoUser = errors.New("user not found in the directory")

func (c *LDAPConfig) findUser(conn *ldap.Conn, username string) (*ldap.Entry, error) {
	filter := strings.ReplaceAll(c.UserFilter, "{username}", ldap.EscapeFilter(username))
	res, err := conn.Search(ldap.NewSearchRequest(c.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(ldapTimeout.Seconds()), false, filter, c.attrs(), nil))
	if err != nil {
		return nil, fmt.Errorf("user search failed: %w", err)
	}
	switch len(res.Entries) {
	case 0:
		return nil, errNoUser
	case 1:
		return res.Entries[0], nil
	default:
		return nil, errors.New("the user filter matches more than one entry")
	}
}

func (c *LDAPConfig) inGroup(conn *ldap.Conn, entry *ldap.Entry, username string) (bool, error) {
	if c.RequiredGroup == "" {
		return true, nil
	}
	group := ldap.EscapeFilter(c.RequiredGroup)
	// 1. memberOf on the user (Active Directory, OpenLDAP with the memberof overlay).
	filter := "(memberOf=" + group + ")"
	if c.NestedGroups {
		filter = "(memberOf:" + adMatchingRuleInChain + ":=" + group + ")"
	}
	res, err := conn.Search(ldap.NewSearchRequest(entry.DN, ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		1, int(ldapTimeout.Seconds()), false, filter, []string{"dn"}, nil))
	if err == nil && len(res.Entries) == 1 {
		return true, nil
	}
	// 2. The group entry lists the user (groupOfNames, groupOfUniqueNames, posixGroup).
	dn := ldap.EscapeFilter(entry.DN)
	filter = "(|(member=" + dn + ")(uniqueMember=" + dn + ")(memberUid=" + ldap.EscapeFilter(username) + "))"
	res, err = conn.Search(ldap.NewSearchRequest(c.RequiredGroup, ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		1, int(ldapTimeout.Seconds()), false, filter, []string{"dn"}, nil))
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			return false, fmt.Errorf("group %s does not exist", c.RequiredGroup)
		}
		return false, fmt.Errorf("group check failed: %w", err)
	}
	return len(res.Entries) == 1, nil
}

func (c *LDAPConfig) identity(e *ldap.Entry, username string) *Identity {
	id := &Identity{Source: "ldap", ExternalID: strings.ToLower(e.DN), Username: username}
	if c.UsernameAttr != "" {
		if v := e.GetAttributeValue(c.UsernameAttr); v != "" {
			id.Username = v
		}
	}
	if c.NameAttr != "" {
		id.DisplayName = e.GetAttributeValue(c.NameAttr)
	}
	if c.EmailAttr != "" {
		id.Email = e.GetAttributeValue(c.EmailAttr)
	}
	return id
}

// ErrBadCredentials means the username or password is wrong.
var ErrBadCredentials = errors.New("invalid username or password")

// Authenticate verifies username and password against the directory. It
// returns ErrBadCredentials or ErrDenied for user errors; other errors are
// configuration or connectivity problems.
func (c *LDAPConfig) Authenticate(username, password string) (*Identity, error) {
	username = strings.TrimSpace(username)
	// An empty password would be an "unauthenticated bind", which many
	// servers accept. Never allow it.
	if username == "" || password == "" {
		return nil, ErrBadCredentials
	}
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := c.serviceBind(conn); err != nil {
		return nil, err
	}
	entry, err := c.findUser(conn, username)
	if errors.Is(err, errNoUser) {
		return nil, ErrBadCredentials
	} else if err != nil {
		return nil, err
	}
	if err := conn.Bind(entry.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrBadCredentials
		}
		return nil, fmt.Errorf("user bind failed: %w", err)
	}
	// Check membership with the service account: users often cannot read groups.
	if err := c.serviceBind(conn); err != nil {
		return nil, err
	}
	ok, err := c.inGroup(conn, entry, username)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDenied
	}
	return c.identity(entry, username), nil
}

// Test connects, binds with the service account and, when username is
// given, looks the user up and reports group membership.
func (c *LDAPConfig) Test(username string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	conn, err := c.dial()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := c.serviceBind(conn); err != nil {
		return "", err
	}
	msg := "Connected and bound successfully"
	if c.BindDN == "" {
		msg = "Connected (anonymous bind)"
	}
	if username = strings.TrimSpace(username); username == "" {
		return msg + ".", nil
	}
	entry, err := c.findUser(conn, username)
	if err != nil {
		return "", err
	}
	id := c.identity(entry, username)
	msg += fmt.Sprintf(". Found %s (%s", entry.DN, id.Username)
	if id.DisplayName != "" {
		msg += ", " + id.DisplayName
	}
	if id.Email != "" {
		msg += ", " + id.Email
	}
	msg += ")"
	if c.RequiredGroup != "" {
		ok, err := c.inGroup(conn, entry, username)
		if err != nil {
			return "", err
		}
		if ok {
			msg += ", member of the required group"
		} else {
			msg += ", NOT a member of the required group"
		}
	}
	return msg + ".", nil
}
