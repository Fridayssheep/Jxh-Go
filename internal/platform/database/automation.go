package database

import (
	"context"

	"gorm.io/gorm"
)

// SchemaAutomation is an optional startup extension point for future
// idempotent schema maintenance. The default runtime does not provide one.
type SchemaAutomation interface {
	Apply(context.Context, *gorm.DB) error
}
