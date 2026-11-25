// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// PushoverKeyRe matches a Pushover user, group or application key.
var PushoverKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)

// ParsePushoverKeys splits and validates a comma, semicolon, space or newline
// separated list of Pushover user or group keys, without duplicates.
func ParsePushoverKeys(list string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, k := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		if !PushoverKeyRe.MatchString(k) {
			return out, fmt.Errorf("%q is not a Pushover user or group key (30 letters and digits)", MaskKey(k))
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// NormalizePushoverKeys validates a list of keys and returns it as "a, b".
func NormalizePushoverKeys(list string) (string, error) {
	keys, err := ParsePushoverKeys(list)
	return strings.Join(keys, ", "), err
}

// MaskKey shortens a key for logs and messages: "uQiR…RsG".
func MaskKey(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:4] + "…" + k[len(k)-3:]
}

// SetMonitorsPushover turns sending to the default Pushover key on or off
// for the given monitors and returns how many changed.
func (s *Store) SetMonitorsPushover(ctx context.Context, ids []int64, on bool) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE monitors SET pushover=$2 WHERE id = ANY($1) AND pushover <> $2`, ids, on)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// PushoverCounts returns how many monitors send to the default Pushover key,
// how many have extra keys, and how many monitors there are.
func (s *Store) PushoverCounts(ctx context.Context) (def, extra, total int, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pushover), count(*) FILTER (WHERE pushover_keys <> ''),
		count(*) FROM monitors`).Scan(&def, &extra, &total)
	return
}
