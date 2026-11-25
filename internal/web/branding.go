// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The slogan shown under the logo and, by default, in the footer.
const defaultSlogan = "Know before your users do."

const (
	brandingKey = "branding"
	// MaxFooterLen bounds the footer line, in characters.
	MaxFooterLen = 200
)

// Branding is the look of the interface that administrators can change
// under Settings → Branding. The "MonitorIQ by Brainslad" credit is not
// part of it: it stays in the footer (GPL-3.0 §7(b) term, see NOTICE).
type Branding struct {
	Footer string `json:"footer"`
}

// FooterLine is the text after the credit in the footer.
func (b Branding) FooterLine() string {
	if b.Footer == "" {
		return defaultSlogan
	}
	return b.Footer
}

// Slogan is the line under the logo; it does not change.
func (b Branding) Slogan() string { return defaultSlogan }

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

// ---------------------------------------------------------------- settings page

func (s *Server) handleBrandingForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "branding", "Branding", "settings", map[string]any{
		"B": s.branding(r.Context()), "Slogan": defaultSlogan, "MaxFooter": MaxFooterLen,
	})
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
	if footer == defaultSlogan {
		b.Footer = ""
	}
	if err := s.saveBranding(ctx, b); err != nil {
		s.serverError(w, err)
		return
	}
	s.flash(w, r, "/admin/branding", true, "Branding saved.")
}
