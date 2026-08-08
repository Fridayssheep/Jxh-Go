package database

import (
	"errors"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
)

func TestIsAlreadyAppliedError(t *testing.T) {
	// "Object already exists" errors let a partially applied migration converge on re-run.
	for _, number := range []uint16{1050, 1060, 1061, 1826} {
		if !isAlreadyAppliedError(&drivermysql.MySQLError{Number: number}) {
			t.Errorf("MySQL error %d should be treated as already applied", number)
		}
	}
	// Genuine failures must never be swallowed, or a broken schema would look migrated.
	for _, number := range []uint16{
		1064, // syntax error
		1146, // table does not exist
		1005, // cannot create table
		1452, // foreign key constraint fails
		1062, // duplicate entry: a data conflict, not DDL idempotency
		1044, // access denied for database
		1142, // command denied
	} {
		if isAlreadyAppliedError(&drivermysql.MySQLError{Number: number}) {
			t.Errorf("MySQL error %d must not be treated as already applied", number)
		}
	}
	if isAlreadyAppliedError(errors.New("connection refused")) {
		t.Error("non-MySQL errors must not be treated as already applied")
	}
	if isAlreadyAppliedError(nil) {
		t.Error("nil must not be treated as already applied")
	}
	wrapped := errors.Join(errors.New("apply statement"), &drivermysql.MySQLError{Number: 1060})
	if !isAlreadyAppliedError(wrapped) {
		t.Error("wrapped MySQL errors should still be classified")
	}
}

func TestSummarizeStatement(t *testing.T) {
	if got := summarizeStatement("ALTER TABLE t\n  ADD COLUMN x JSON NULL;"); got != "ALTER TABLE t ADD COLUMN x JSON NULL;" {
		t.Errorf("summarizeStatement collapsed incorrectly: %q", got)
	}
	long := summarizeStatement("CREATE TABLE z (" + strings.Repeat("col INT, ", 40) + ")")
	if !strings.HasSuffix(long, "...") || len(long) > 123 {
		t.Errorf("long statements should truncate, got length %d: %q", len(long), long)
	}
}

func TestSplitMigrationStatementsSkipsComments(t *testing.T) {
	statements := splitMigrationStatements(`-- leading comment
ALTER TABLE a ADD COLUMN b JSON NULL;

-- another comment
CREATE TABLE c (id INT);
`)
	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	for index, statement := range statements {
		if strings.Contains(statement, "--") {
			t.Errorf("statement %d retained a comment: %q", index+1, statement)
		}
	}
}

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/002_join_approval_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	statements := splitMigrationStatements(string(content))
	if len(statements) == 0 {
		t.Fatal("migration 002 produced no statements")
	}
	for index, statement := range statements {
		// ADD COLUMN IF NOT EXISTS is MariaDB-only and is a syntax error on MySQL.
		if strings.Contains(statement, "ADD COLUMN IF NOT EXISTS") {
			t.Errorf("statement %d uses MariaDB-only ADD COLUMN IF NOT EXISTS: %s",
				index+1, summarizeStatement(statement))
		}
		if strings.HasPrefix(statement, "CREATE TABLE ") && !strings.Contains(statement, "IF NOT EXISTS") {
			t.Errorf("statement %d should use CREATE TABLE IF NOT EXISTS: %s",
				index+1, summarizeStatement(statement))
		}
	}
}
