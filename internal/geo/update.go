// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package geo

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// DefaultMirror is where the free DB-IP Lite databases are published
// (monthly, licensed CC BY 4.0: pages that show the data credit DB-IP).
const DefaultMirror = "https://download.db-ip.com/free"

// maxDBSize bounds a decompressed database file.
const maxDBSize = 1 << 30

// UpdateResult reports one downloaded file.
type UpdateResult struct {
	File    string
	URL     string
	Type    string
	Built   time.Time
	Size    int64
	Skipped bool // the file already had this month's edition
}

// Update downloads this month's DB-IP Lite city and ASN databases into the
// database directory (falling back to last month's while the new edition is
// not published yet) and reloads them. mirror may be empty for DefaultMirror.
func (d *DB) Update(ctx context.Context, client *http.Client, mirror string, now time.Time) ([]UpdateResult, error) {
	if mirror == "" {
		mirror = DefaultMirror
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if err := os.MkdirAll(d.dir, 0o750); err != nil {
		return nil, err
	}
	var out []UpdateResult
	for _, f := range []struct{ kind, file string }{{"city", CityFile}, {"asn", ASNFile}} {
		res, err := d.fetch(ctx, client, mirror, f.kind, f.file, now)
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, d.Reload()
}

func (d *DB) fetch(ctx context.Context, client *http.Client, mirror, kind, file string, now time.Time) (UpdateResult, error) {
	dest := filepath.Join(d.dir, file)
	var lastErr error
	for _, month := range []time.Time{now, now.AddDate(0, -1, 0)} {
		url := fmt.Sprintf("%s/dbip-%s-lite-%s.mmdb.gz", mirror, kind, month.Format("2006-01"))
		// Skip the download when the file on disk is already this edition.
		if cur, err := maxminddb.Open(dest); err == nil {
			built := time.Unix(int64(cur.Metadata.BuildEpoch), 0).UTC()
			_ = cur.Close()
			if built.Year() == month.Year() && built.Month() == month.Month() {
				st, _ := os.Stat(dest)
				return UpdateResult{File: file, URL: url, Built: built, Size: st.Size(), Skipped: true}, nil
			}
		}
		res, err := download(ctx, client, url, dest)
		if err == nil {
			res.File = file
			return res, nil
		}
		lastErr = err
		if !errors.Is(err, errNotPublished) {
			break
		}
	}
	return UpdateResult{}, fmt.Errorf("%s database: %w", kind, lastErr)
}

var errNotPublished = errors.New("not published")

// download fetches a gzip'ed database, checks that it opens, and moves it in place.
func download(ctx context.Context, client *http.Client, url, dest string) (UpdateResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return UpdateResult{}, err
	}
	req.Header.Set("User-Agent", "MonitorIQ (self-hosted uptime monitor)")
	resp, err := client.Do(req)
	if err != nil {
		return UpdateResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return UpdateResult{}, fmt.Errorf("%s: %w", url, errNotPublished)
	}
	if resp.StatusCode != http.StatusOK {
		return UpdateResult{}, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("%s: %w", url, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.mmdb")
	if err != nil {
		return UpdateResult{}, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(zr, maxDBSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return UpdateResult{}, fmt.Errorf("%s: %w", url, err)
	}
	if n > maxDBSize {
		return UpdateResult{}, fmt.Errorf("%s: file is larger than expected", url)
	}
	r, err := maxminddb.Open(tmp.Name())
	if err != nil {
		return UpdateResult{}, fmt.Errorf("%s: not a valid database: %w", url, err)
	}
	res := UpdateResult{URL: url, Type: r.Metadata.DatabaseType, Built: time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC(), Size: n}
	_ = r.Close()
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return UpdateResult{}, err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return UpdateResult{}, err
	}
	return res, nil
}
