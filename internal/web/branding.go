// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The slogan shown under the logo.
const defaultSlogan = "Know before your users do."

const (
	brandingKey = "branding"
	// The logo images are kept in their own settings rows (base64).
	logoKey     = "branding_logo"
	logoDarkKey = "branding_logo_dark"
	// MaxUploadBytes bounds an uploaded logo file; PNG, JPEG and GIF
	// images are then shrunk to the logo box (see fitLogo).
	MaxUploadBytes = 5 << 20
	// MaxLogoBytes bounds a stored logo (an SVG or WebP is stored as sent).
	MaxLogoBytes = 512 << 10
	// MaxFooterLen bounds the footer line, in characters.
	MaxFooterLen = 200
)

// logoInfo describes a stored logo image.
type logoInfo struct {
	Type string `json:"type"` // image/png, image/svg+xml, …
	Sum  string `json:"sum"`  // short hash, used to bust caches
}

// Branding is the look of the interface that administrators can change
// under Settings → Branding. The "MonitorIQ by Brainslad" credit is not
// part of it: it stays in the footer (GPL-3.0 §7(b) term, see NOTICE).
type Branding struct {
	Footer   string    `json:"footer"`
	Logo     *logoInfo `json:"logo,omitempty"`
	LogoDark *logoInfo `json:"logo_dark,omitempty"`
}

// FooterLine is the custom line shown above the credit in the footer
// ("" when none is set: the footer then shows only the credit).
func (b Branding) FooterLine() string { return b.Footer }

// Slogan is the line under the logo; it does not change.
func (b Branding) Slogan() string { return defaultSlogan }

// LogoURL is the address of the custom logo ("" for the built-in one).
func (b Branding) LogoURL() string {
	if b.Logo == nil {
		return ""
	}
	return "/branding/logo?v=" + b.Logo.Sum
}

// LogoDarkURL is the logo for the dark theme (the main logo when none).
func (b Branding) LogoDarkURL() string {
	if b.LogoDark == nil {
		return b.LogoURL()
	}
	return "/branding/logo-dark?v=" + b.LogoDark.Sum
}

// brandCache keeps the branding in memory; it is read on every page.
type brandCache struct {
	mu     sync.RWMutex
	loaded bool
	b      Branding
}

func (s *Server) branding(ctx context.Context) Branding {
	s.brand.mu.RLock()
	if s.brand.loaded {
		b := s.brand.b
		s.brand.mu.RUnlock()
		return b
	}
	s.brand.mu.RUnlock()
	var b Branding
	raw, err := s.Store.GetSetting(ctx, brandingKey)
	if err != nil {
		s.Log.Error("branding", "err", err)
		return b
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			s.Log.Error("branding", "err", err)
		}
	}
	s.brand.mu.Lock()
	s.brand.b, s.brand.loaded = b, true
	s.brand.mu.Unlock()
	return b
}

func (s *Server) saveBranding(ctx context.Context, b Branding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, brandingKey, string(raw)); err != nil {
		return err
	}
	s.brand.mu.Lock()
	s.brand.b, s.brand.loaded = b, true
	s.brand.mu.Unlock()
	return nil
}

// cleanFooter trims the footer line and rejects control characters.
func cleanFooter(v string) (string, error) {
	v = strings.Join(strings.Fields(v), " ")
	if utf8.RuneCountInString(v) > MaxFooterLen {
		return "", errors.New("the footer line can be at most 200 characters long")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", errors.New("the footer line contains invalid characters")
		}
	}
	return v, nil
}

var errLogoType = errors.New("the logo must be a PNG, JPEG, GIF, WebP or SVG image")

// logoType checks an uploaded image and returns its media type.
func logoType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the logo file is empty")
	}
	if len(data) > MaxUploadBytes {
		return "", errors.New("the logo file can be at most 5 MB")
	}
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct, nil
	}
	if isSVG(data) {
		return "image/svg+xml", nil
	}
	return "", errLogoType
}

// isSVG reports whether data is an XML document whose root element is <svg>.
func isSVG(data []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return strings.EqualFold(se.Name.Local, "svg")
		}
	}
}

func logoSum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:6])
}

// handleLogo serves a custom logo. It is public: the sign-in page shows it.
// Images are shown through <img>, which never runs scripts; the CSP below
// also blocks scripts when an SVG logo is opened directly.
func (s *Server) handleLogo(dark bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := s.branding(r.Context())
		info, key := b.Logo, logoKey
		if dark && b.LogoDark != nil {
			info, key = b.LogoDark, logoDarkKey
		}
		if info == nil {
			http.NotFound(w, r)
			return
		}
		raw, err := s.Store.GetSetting(r.Context(), key)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", info.Type)
		h.Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
		h.Set("Content-Disposition", "inline")
		if r.URL.Query().Get("v") == info.Sum {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		_, _ = w.Write(data)
	}
}

// ---------------------------------------------------------------- settings page

func (s *Server) handleBrandingForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "branding", "Branding", "settings", map[string]any{
		"B": s.branding(r.Context()), "Slogan": defaultSlogan, "MaxMB": MaxUploadBytes >> 20, "MaxFooter": MaxFooterLen,
	})
}

// readLogo reads an uploaded logo field; it returns nil when none was sent.
func readLogo(r *http.Request, field string) ([]byte, string, error) {
	f, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", errors.New("the logo could not be read")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxUploadBytes+1))
	if err != nil {
		return nil, "", errors.New("the logo could not be read")
	}
	if len(data) == 0 {
		return nil, "", nil
	}
	ct, err := logoType(data)
	if err != nil {
		return nil, "", err
	}
	if data, ct, err = fitLogo(data, ct); err != nil {
		return nil, "", err
	}
	if len(data) > MaxLogoBytes {
		return nil, "", errors.New("an SVG or WebP logo can be at most 512 KB")
	}
	return data, ct, nil
}

func (s *Server) handleBrandingSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b := s.branding(ctx)
	footer, err := cleanFooter(r.PostFormValue("footer"))
	if err != nil {
		s.flash(w, r, "/admin/branding", false, capitalize(err.Error())+".")
		return
	}
	b.Footer = footer
	for _, l := range []struct {
		field, remove, key string
		info               **logoInfo
	}{{"logo", "remove_logo", logoKey, &b.Logo}, {"logo_dark", "remove_logo_dark", logoDarkKey, &b.LogoDark}} {
		data, ct, err := readLogo(r, l.field)
		if err != nil {
			s.flash(w, r, "/admin/branding", false, capitalize(err.Error())+".")
			return
		}
		switch {
		case data != nil:
			if err := s.Store.SetSetting(ctx, l.key, base64.StdEncoding.EncodeToString(data)); err != nil {
				s.serverError(w, err)
				return
			}
			*l.info = &logoInfo{Type: ct, Sum: logoSum(data)}
		case formBool(r, l.remove):
			if err := s.Store.SetSetting(ctx, l.key, ""); err != nil {
				s.serverError(w, err)
				return
			}
			*l.info = nil
		}
	}
	if err := s.saveBranding(ctx, b); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/branding", true, "Branding saved.")
}

// limitBody caps the size of a request body (uploads). Bodies that say they
// are too large are turned away with a message on back.
func (s *Server) limitBody(n int64, back string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			s.flash(w, r, back, false, "The upload is too large: a logo file can be at most 5 MB.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}
