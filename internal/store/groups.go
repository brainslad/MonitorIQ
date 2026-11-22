// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// GroupColors are the colours a group can have, in the order they are offered.
var GroupColors = []string{"steel", "teal", "green", "amber", "orange", "red", "violet", "slate"}

// ValidGroupColor reports whether c is one of GroupColors.
func ValidGroupColor(c string) bool {
	for _, x := range GroupColors {
		if x == c {
			return true
		}
	}
	return false
}

// Group gathers monitors, for example by service, team or customer.
type Group struct {
	ID          int64
	Name        string
	Description string
	Color       string
	CreatedAt   time.Time
	// Counts of the group's monitors (filled by ListGroups).
	Monitors, Up, Degraded, Down, Unknown, Paused int
}

// Status is the worst status among the group's enabled monitors, as a CSS
// class ("down", "degraded", "up", "unknown").
func (g Group) Status() string {
	switch {
	case g.Down > 0:
		return "down"
	case g.Degraded > 0:
		return "degraded"
	case g.Up > 0:
		return "up"
	}
	return "unknown"
}

// Active is the number of enabled monitors.
func (g Group) Active() int { return g.Monitors - g.Paused }

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.Pool.Query(ctx, `SELECT g.id, g.name, g.description, g.color, g.created_at,
		count(m.id),
		count(m.id) FILTER (WHERE m.enabled AND m.status = 1),
		count(m.id) FILTER (WHERE m.enabled AND m.status = 2),
		count(m.id) FILTER (WHERE m.enabled AND m.status = 3),
		count(m.id) FILTER (WHERE m.enabled AND m.status = 0),
		count(m.id) FILTER (WHERE NOT m.enabled)
		FROM monitor_groups g LEFT JOIN monitors m ON m.group_id = g.id
		GROUP BY g.id ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Group, error) {
		var g Group
		err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Color, &g.CreatedAt,
			&g.Monitors, &g.Up, &g.Degraded, &g.Down, &g.Unknown, &g.Paused)
		return g, err
	})
}

func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	var g Group
	err := s.Pool.QueryRow(ctx, `SELECT id, name, description, color, created_at FROM monitor_groups WHERE id=$1`, id).
		Scan(&g.ID, &g.Name, &g.Description, &g.Color, &g.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &g, nil
}

// ValidateGroup normalises and validates user input.
func ValidateGroup(g *Group) error {
	g.Name = strings.Join(strings.Fields(g.Name), " ")
	g.Description = strings.TrimSpace(g.Description)
	g.Color = strings.TrimSpace(g.Color)
	if g.Color == "" {
		g.Color = GroupColors[0]
	}
	switch {
	case g.Name == "":
		return errors.New("group name is required")
	case utf8.RuneCountInString(g.Name) > 80:
		return errors.New("group name is too long (80 characters at most)")
	case utf8.RuneCountInString(g.Description) > 500:
		return errors.New("description is too long (500 characters at most)")
	case !ValidGroupColor(g.Color):
		return errors.New("invalid group colour")
	}
	return nil
}

// ErrGroupExists is returned when another group already has the name.
var ErrGroupExists = errors.New("a group with this name already exists")

func (s *Store) SaveGroup(ctx context.Context, g *Group) error {
	if err := ValidateGroup(g); err != nil {
		return err
	}
	var err error
	if g.ID == 0 {
		err = s.Pool.QueryRow(ctx, `INSERT INTO monitor_groups (name, description, color) VALUES ($1,$2,$3) RETURNING id`,
			g.Name, g.Description, g.Color).Scan(&g.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.Pool.Exec(ctx, `UPDATE monitor_groups SET name=$2, description=$3, color=$4 WHERE id=$1`,
			g.ID, g.Name, g.Description, g.Color)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrGroupExists
	}
	return err
}

// DeleteGroup removes a group; its monitors become ungrouped.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM monitor_groups WHERE id=$1`, id)
	return err
}

// SetMonitorsGroup moves monitors to a group (groupID 0 removes them from
// their group) and returns how many were changed.
func (s *Store) SetMonitorsGroup(ctx context.Context, ids []int64, groupID int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var gid *int64
	if groupID != 0 {
		if _, err := s.GetGroup(ctx, groupID); err != nil {
			return 0, err
		}
		gid = &groupID
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE monitors SET group_id=$2 WHERE id = ANY($1) AND group_id IS DISTINCT FROM $2`, ids, gid)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
