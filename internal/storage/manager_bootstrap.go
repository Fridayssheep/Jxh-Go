package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ auth.BootstrapStore = (*Store)(nil)

// CreateFirstSuperAdmin serializes bootstrap attempts on the migration baseline
// row. The lock remains held until the user and its audit record commit.
func (s *Store) CreateFirstSuperAdmin(ctx context.Context, admin auth.BootstrapAdmin) (auth.User, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.User{}, false, err
	}
	if admin.User.ID != "" || admin.User.Username == "" || admin.User.DisplayName == "" ||
		admin.User.Role != auth.RoleSuperAdmin || !admin.User.Enabled || admin.User.Version != 1 || admin.PasswordHash == "" {
		return auth.User{}, false, managerFailure("validate administrator bootstrap", nil)
	}

	var created auth.User
	var inserted bool
	err = managerTransaction(db, "create first super administrator", func(tx *gorm.DB) error {
		var migration struct {
			Version uint64 `gorm:"column:version"`
		}
		result := tx.Table("schema_migrations").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("version").Order("version ASC").Limit(1).Take(&migration)
		if result.Error != nil {
			return result.Error
		}

		var existing managerAdminUser
		result = tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("user_id").Order("user_id ASC").Limit(1).Take(&existing)
		if result.Error == nil {
			return nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}

		userID, err := newManagerAuthID("usr_")
		if err != nil {
			return err
		}
		auditID, err := newManagerAuthID("aud_")
		if err != nil {
			return err
		}
		now := normalizedManagerTime(time.Now())
		row := managerAdminUser{
			UserID: userID, Username: admin.User.Username, DisplayName: admin.User.DisplayName,
			PasswordHash: admin.PasswordHash, Role: auth.RoleSuperAdmin, Enabled: true,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		after, err := json.Marshal(managerUserSnapshot(row, row.Revision))
		if err != nil {
			return err
		}
		targetType := "admin_user"
		targetID := row.UserID
		targetDisplay := row.DisplayName
		auditRow := managerAuditLog{
			AuditLogID: auditID, OccurredAt: now, ActorType: audit.ActorSystem,
			Action: "user.bootstrap", TargetType: &targetType, TargetID: &targetID,
			TargetDisplay: &targetDisplay, Result: audit.ResultSuccess,
			RequestID: auditID, Source: audit.SourceSystem, AfterSnapshot: after,
			Metadata: json.RawMessage(`{}`), Redacted: true,
		}
		if err := tx.Create(&auditRow).Error; err != nil {
			return err
		}
		created = userFromManagerRow(row)
		inserted = true
		return nil
	})
	if err != nil {
		return auth.User{}, false, err
	}
	return created, inserted, nil
}
