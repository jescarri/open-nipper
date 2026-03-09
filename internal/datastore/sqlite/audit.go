package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/open-nipper/open-nipper/internal/models"
)

// LogAdminAction appends an audit entry to the admin_audit table.
func (s *Store) LogAdminAction(ctx context.Context, entry models.AdminAuditEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_audit (timestamp, action, actor, target_user_id, details, ip_address)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		entry.Timestamp.UTC().Format(time.RFC3339Nano),
		entry.Action, entry.Actor,
		entry.TargetUserID, entry.Details, entry.IPAddress,
	)
	if err != nil {
		return fmt.Errorf("log admin action: %w", err)
	}
	return nil
}

// QueryAuditLog returns audit entries matching the provided filters.
func (s *Store) QueryAuditLog(ctx context.Context, filters models.AuditQueryFilters) ([]*models.AdminAuditEntry, error) {
	where := []string{}
	args := []any{}

	if filters.Since != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, filters.Since.UTC().Format(time.RFC3339Nano))
	}
	if filters.Until != nil {
		where = append(where, "timestamp <= ?")
		args = append(args, filters.Until.UTC().Format(time.RFC3339Nano))
	}
	if filters.Action != "" {
		where = append(where, "action = ?")
		args = append(args, filters.Action)
	}
	if filters.TargetUserID != "" {
		where = append(where, "target_user_id = ?")
		args = append(args, filters.TargetUserID)
	}

	query := `SELECT id, timestamp, action, actor, target_user_id, details, ip_address
	          FROM admin_audit`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp DESC"
	if filters.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filters.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	defer rows.Close()

	var entries []*models.AdminAuditEntry
	for rows.Next() {
		var e models.AdminAuditEntry
		var ts, targetUserID, ipAddress string
		if err := rows.Scan(&e.ID, &ts, &e.Action, &e.Actor, &targetUserID, &e.Details, &ipAddress); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		e.TargetUserID = targetUserID
		e.IPAddress = ipAddress
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
