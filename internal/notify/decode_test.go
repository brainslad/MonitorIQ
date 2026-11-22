// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package notify

import "mime"

type mimeDecoder struct{}

func (mimeDecoder) decode(s string) (string, error) { return new(mime.WordDecoder).DecodeHeader(s) }
