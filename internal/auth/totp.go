package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// TOTP parameters (RFC 6238 defaults, understood by every authenticator app).
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // accept one step before and after to absorb clock drift
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 encoded.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	return b32.DecodeString(s)
}

func hotp(key []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// TOTPCode returns the code for secret at time t.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, t.Unix()/totpPeriod), nil
}

// ValidateTOTP checks code against secret at time t and returns the matched
// time step, which callers store to refuse replays of the same code.
func ValidateTOTP(secret, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	step := t.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		if hmac.Equal([]byte(hotp(key, step+d)), []byte(code)) {
			return step + d, true
		}
	}
	return 0, false
}

// TOTPURI builds the otpauth:// URI encoded in the enrolment QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// QRDataURI renders text as a PNG QR code data: URI.
func QRDataURI(text string) (string, error) {
	png, err := qrcode.Encode(text, qrcode.Medium, 240)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// GroupSecret formats a secret in groups of four for manual entry.
func GroupSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NewRecoveryCodes returns n single-use recovery codes like "k7qm2-x9fd4".
func NewRecoveryCodes(n int) []string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	codes := make([]string, n)
	buf := make([]byte, 10)
	for i := range codes {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		c := make([]byte, 0, 11)
		for j, b := range buf {
			if j == 5 {
				c = append(c, '-')
			}
			c = append(c, alphabet[int(b)%len(alphabet)])
		}
		codes[i] = string(c)
	}
	return codes
}
