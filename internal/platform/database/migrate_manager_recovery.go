package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

var repositoryMigration008Identity = historicalMigrationIdentity{
	name:     "008_create_manager_schema",
	checksum: "a52e9d085d265ebb39339e57931d95bbc396f2a4c3b675559b9dec0430a25db9",
}

const (
	managerMigration008GroupStage2Fingerprint          = "e022dfa641d79d9eb275cec2fcb9e4b167da90303250525d6d0b85acf8572f32"
	managerMigration008RecoveredGroupStage2Fingerprint = "9ecda57f542ef79899edafc071ed7eea018b6574881cce987bfd8c37d3dd9824"
)

const managerMigration008TableFingerprintQuery = `SELECT SHA2(CONCAT(
  'table:', COALESCE((
    SELECT CONCAT_WS(':',
      CONCAT('V', HEX(table_type)),
      IF(engine IS NULL, 'N', CONCAT('V', HEX(engine))),
      IF(table_collation IS NULL, 'N', CONCAT('V', HEX(table_collation)))
    )
    FROM information_schema.tables
    WHERE table_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), '!missing'),
  '|columns:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(column_name)), ordinal_position,
      CONCAT('V', HEX(column_type)), CONCAT('V', HEX(is_nullable)),
      IF(column_default IS NULL, 'N', CONCAT('V', HEX(column_default))),
      IF(character_set_name IS NULL, 'N', CONCAT('V', HEX(character_set_name))),
      IF(collation_name IS NULL, 'N', CONCAT('V', HEX(collation_name))),
      CONCAT('V', HEX(extra)), CONCAT('V', HEX(generation_expression)),
      CONCAT('V', HEX(column_comment))
    ) ORDER BY ordinal_position SEPARATOR '|')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), ''),
  '|indexes:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(index_name)), non_unique, seq_in_index,
      IF(column_name IS NULL, 'N', CONCAT('V', HEX(column_name))),
      IF(collation IS NULL, 'N', CONCAT('V', HEX(collation))),
      IF(sub_part IS NULL, 'N', CONCAT('V', sub_part)),
      CONCAT('V', HEX(nullable)), CONCAT('V', HEX(index_type)),
      CONCAT('V', HEX(comment)), CONCAT('V', HEX(index_comment)),
      CONCAT('V', HEX(is_visible)),
      IF(expression IS NULL, 'N', CONCAT('V', HEX(expression)))
    ) ORDER BY index_name, seq_in_index SEPARATOR '|')
    FROM information_schema.statistics
    WHERE table_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), ''),
  '|constraints:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(constraint_name)), CONCAT('V', HEX(constraint_type)),
      CONCAT('V', HEX(enforced))
    ) ORDER BY constraint_name SEPARATOR '|')
    FROM information_schema.table_constraints
    WHERE constraint_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), ''),
  '|keys:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(constraint_name)), ordinal_position,
      IF(position_in_unique_constraint IS NULL, 'N', CONCAT('V', position_in_unique_constraint)),
      CONCAT('V', HEX(column_name)),
      IF(referenced_table_name IS NULL, 'N', CONCAT('V', HEX(referenced_table_name))),
      IF(referenced_column_name IS NULL, 'N', CONCAT('V', HEX(referenced_column_name)))
    ) ORDER BY constraint_name, ordinal_position SEPARATOR '|')
    FROM information_schema.key_column_usage
    WHERE constraint_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), ''),
  '|references:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(constraint_name)), CONCAT('V', HEX(unique_constraint_name)),
      CONCAT('V', HEX(match_option)), CONCAT('V', HEX(update_rule)),
      CONCAT('V', HEX(delete_rule))
    ) ORDER BY constraint_name SEPARATOR '|')
    FROM information_schema.referential_constraints
    WHERE constraint_schema = DATABASE() AND BINARY table_name = BINARY ?
  ), ''),
  '|checks:', COALESCE((
    SELECT GROUP_CONCAT(CONCAT_WS(':',
      CONCAT('V', HEX(tc.constraint_name)), CONCAT('V', HEX(cc.check_clause))
    ) ORDER BY tc.constraint_name SEPARATOR '|')
    FROM information_schema.table_constraints AS tc
    JOIN information_schema.check_constraints AS cc
      ON cc.constraint_schema = tc.constraint_schema
     AND cc.constraint_name = tc.constraint_name
    WHERE tc.constraint_schema = DATABASE()
      AND BINARY tc.table_name = BINARY ?
      AND BINARY tc.constraint_type = BINARY 'CHECK'
  ), '')
), 256)`

type managerMigration008CommentSpec struct {
	table        string
	column       string
	definition   string
	columnType   string
	collation    string
	nullable     string
	defaultValue *string
	canonical    string
	legacy       string
}

var managerMigration008CommentSpecs = []managerMigration008CommentSpec{
	{table: "group_join_requests", column: "flag", definition: "varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL", columnType: "varchar(512)", collation: "utf8mb4_bin", nullable: "NO", canonical: "NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串", legacy: "NapCat 群通知标识；实时事件取 flag，系统消息取 request_id 字符串"},
	{table: "group_join_requests", column: "group_id", definition: "bigint DEFAULT NULL", columnType: "bigint", nullable: "YES", canonical: "QQ群号"},
	{table: "group_join_requests", column: "user_id", definition: "bigint DEFAULT NULL", columnType: "bigint", nullable: "YES", canonical: "申请人 QQ"},
	{table: "group_join_requests", column: "student_id", definition: "varchar(64) DEFAULT NULL", columnType: "varchar(64)", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "申请信息中显式填写的学号"},
	{table: "group_join_requests", column: "student_name", definition: "varchar(64) DEFAULT NULL", columnType: "varchar(64)", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "申请信息中显式填写的姓名"},
	{table: "group_join_requests", column: "major", definition: "varchar(128) DEFAULT NULL", columnType: "varchar(128)", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "AI 从验证信息中提取的专业"},
	{table: "group_join_requests", column: "comment", definition: "text DEFAULT NULL", columnType: "text", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "申请验证信息"},
	{table: "group_join_requests", column: "raw_json", definition: "mediumtext DEFAULT NULL", columnType: "mediumtext", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "NapCat 原始事件或系统消息 JSON"},
	{table: "group_join_requests", column: "system_raw_json", definition: "mediumtext DEFAULT NULL", columnType: "mediumtext", collation: "utf8mb4_0900_ai_ci", nullable: "YES", canonical: "NapCat 最近一次系统消息 JSON"},
	{table: "group_join_requests", column: "ai_parse_attempts", definition: "int unsigned NOT NULL DEFAULT 0", columnType: "int unsigned", nullable: "NO", defaultValue: stringPointer("0"), canonical: "AI 解析尝试次数"},
	{table: "group_join_requests", column: "requested_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "申请时间"},
	{table: "group_join_requests", column: "processed_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "首次观察到已处理状态的时间"},
	{table: "group_join_requests", column: "first_seen_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "首次登记时间"},
	{table: "group_join_requests", column: "last_seen_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "最近出现时间"},
	{table: "group_join_requests", column: "ai_parsed_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "AI 解析完成时间"},
	{table: "scheduled_jobs", column: "type", definition: "varchar(16) NOT NULL", columnType: "varchar(16)", collation: "utf8mb4_0900_ai_ci", nullable: "NO", canonical: "任务类型：每天/单次"},
	{table: "scheduled_jobs", column: "time_hhmm", definition: "varchar(5) NOT NULL", columnType: "varchar(5)", collation: "utf8mb4_0900_ai_ci", nullable: "NO", canonical: "触发时间，格式 HH:MM"},
	{table: "scheduled_jobs", column: "run_date", definition: "date DEFAULT NULL", columnType: "date", nullable: "YES", canonical: "单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL"},
	{table: "scheduled_jobs", column: "group_id", definition: "bigint NOT NULL", columnType: "bigint", nullable: "NO", canonical: "QQ群号"},
	{table: "scheduled_jobs", column: "message", definition: "text NOT NULL", columnType: "text", collation: "utf8mb4_0900_ai_ci", nullable: "NO", canonical: "定时发送内容"},
	{table: "scheduled_jobs", column: "enabled", definition: "boolean NOT NULL", columnType: "tinyint(1)", nullable: "NO", canonical: "是否启用"},
	{table: "scheduled_jobs", column: "last_run_at", definition: "datetime(3) DEFAULT NULL", columnType: "datetime(3)", nullable: "YES", canonical: "最近执行时间；用于防止同一天重复触发"},
}

func isRepositoryMigration008(migration Migration) bool {
	if migration.Version != 8 || migration.Name != repositoryMigration008Identity.name ||
		migration.Checksum != repositoryMigration008Identity.checksum {
		return false
	}
	sum := sha256.Sum256([]byte(migration.SQL))
	return hex.EncodeToString(sum[:]) == repositoryMigration008Identity.checksum
}

func prepareManagerMigration008Recovery(ctx context.Context, conn *sql.Conn, migration Migration) (string, error) {
	partial, err := knownManagerMigration008PartialStage(ctx, conn)
	if err != nil || !partial {
		return migration.SQL, err
	}
	state, err := inspectManagerMigration008Comments(ctx, conn)
	if err != nil {
		return "", err
	}
	switch state {
	case managerCommentsCanonical:
		fingerprint, err := managerMigration008TableFingerprint(ctx, conn, "group_join_requests")
		if err != nil {
			return "", err
		}
		switch fingerprint {
		case managerMigration008GroupStage2Fingerprint:
			return migration.SQL, nil
		case managerMigration008RecoveredGroupStage2Fingerprint:
		default:
			return "", fmt.Errorf("%w: migration 008 canonical partial metadata fingerprint is not recognized", ErrDrift)
		}
	case managerCommentsDoubleEncoded:
		if err := normalizeManagerMigration008Comments(ctx, conn); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("%w: migration 008 partial metadata comments are not recognized", ErrDrift)
	}
	if strings.Count(migration.SQL, managerMigration008GroupStage2Fingerprint) != 3 {
		return "", fmt.Errorf("%w: migration 008 stage fingerprint is unavailable", ErrManifest)
	}
	return strings.ReplaceAll(
		migration.SQL,
		managerMigration008GroupStage2Fingerprint,
		managerMigration008RecoveredGroupStage2Fingerprint,
	), nil
}

func managerMigration008TableFingerprint(ctx context.Context, conn *sql.Conn, table string) (string, error) {
	if _, err := conn.ExecContext(ctx, "SET SESSION group_concat_max_len = 1048576"); err != nil {
		return "", safeDatabaseError("configure migration 008 fingerprint query", err)
	}
	args := make([]any, 7)
	for index := range args {
		args[index] = table
	}
	var fingerprint sql.NullString
	if err := conn.QueryRowContext(ctx, managerMigration008TableFingerprintQuery, args...).Scan(&fingerprint); err != nil {
		return "", safeDatabaseError("inspect migration 008 table fingerprint", err)
	}
	if !fingerprint.Valid {
		return "", fmt.Errorf("%w: migration 008 table fingerprint is unavailable", ErrDrift)
	}
	return fingerprint.String, nil
}

func knownManagerMigration008PartialStage(ctx context.Context, conn *sql.Conn) (bool, error) {
	var groupColumns, groupIndexes, groupConstraints int
	var scheduledColumns, scheduledIndexes, scheduledConstraints, managerTables int
	err := conn.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND BINARY table_name = BINARY 'group_join_requests'),
  (SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema = DATABASE() AND BINARY table_name = BINARY 'group_join_requests'),
  (SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND BINARY table_name = BINARY 'group_join_requests'),
  (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND BINARY table_name = BINARY 'scheduled_jobs'),
  (SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema = DATABASE() AND BINARY table_name = BINARY 'scheduled_jobs'),
  (SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND BINARY table_name = BINARY 'scheduled_jobs'),
  (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN
    ('admin_users', 'admin_sessions', 'admin_audit_logs', 'admin_idempotency_keys', 'managed_groups', 'feature_settings',
     'group_join_policies', 'custom_commands', 'custom_command_runs', 'group_join_decisions', 'scheduled_job_runs',
     'bot_operation_events', 'bot_operation_daily', 'system_operations'))`).Scan(
		&groupColumns, &groupIndexes, &groupConstraints,
		&scheduledColumns, &scheduledIndexes, &scheduledConstraints, &managerTables,
	)
	if err != nil {
		return false, safeDatabaseError("inspect migration 008 partial stage", err)
	}
	return groupColumns == 29 && groupIndexes == 10 && groupConstraints == 10 &&
		scheduledColumns == 22 && scheduledIndexes == 3 && scheduledConstraints == 1 && managerTables == 0, nil
}

type managerMigration008CommentState uint8

const (
	managerCommentsUnknown managerMigration008CommentState = iota
	managerCommentsCanonical
	managerCommentsDoubleEncoded
)

func inspectManagerMigration008Comments(ctx context.Context, conn *sql.Conn) (managerMigration008CommentState, error) {
	rows, err := conn.QueryContext(ctx, `SELECT table_name, column_name, column_type,
       COALESCE(collation_name, ''), is_nullable, column_default, extra, column_comment
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND ((BINARY table_name = BINARY 'group_join_requests' AND column_name IN
    ('flag', 'group_id', 'user_id', 'student_id', 'student_name', 'major', 'comment', 'raw_json', 'system_raw_json',
     'ai_parse_attempts', 'requested_at', 'processed_at', 'first_seen_at', 'last_seen_at', 'ai_parsed_at'))
   OR (BINARY table_name = BINARY 'scheduled_jobs' AND column_name IN
    ('type', 'time_hhmm', 'run_date', 'group_id', 'message', 'enabled', 'last_run_at')))
ORDER BY table_name, ordinal_position`)
	if err != nil {
		return managerCommentsUnknown, safeDatabaseError("inspect migration 008 comments", err)
	}
	defer rows.Close()
	expected := make(map[string]managerMigration008CommentSpec, len(managerMigration008CommentSpecs))
	for _, spec := range managerMigration008CommentSpecs {
		expected[spec.table+"\x00"+spec.column] = spec
	}
	canonical, corrupted, count := true, true, 0
	for rows.Next() {
		var table, column, columnType, collation, nullable, extra, comment string
		var defaultValue sql.NullString
		if err := rows.Scan(&table, &column, &columnType, &collation, &nullable, &defaultValue, &extra, &comment); err != nil {
			return managerCommentsUnknown, safeDatabaseError("inspect migration 008 comment", err)
		}
		spec, ok := expected[table+"\x00"+column]
		if !ok || columnType != spec.columnType || collation != spec.collation || nullable != spec.nullable || extra != "" ||
			!sameNullableString(defaultValue, spec.defaultValue) {
			return managerCommentsUnknown, nil
		}
		canonical = canonical && comment == spec.canonical
		doubleEncoded := comment == doubleEncodedUTF8(spec.canonical)
		if spec.legacy != "" {
			doubleEncoded = doubleEncoded || comment == doubleEncodedUTF8(spec.legacy)
		}
		corrupted = corrupted && doubleEncoded
		count++
	}
	if err := rows.Err(); err != nil {
		return managerCommentsUnknown, safeDatabaseError("inspect migration 008 comments", err)
	}
	if count != len(managerMigration008CommentSpecs) {
		return managerCommentsUnknown, nil
	}
	if canonical {
		return managerCommentsCanonical, nil
	}
	if corrupted {
		return managerCommentsDoubleEncoded, nil
	}
	return managerCommentsUnknown, nil
}

func normalizeManagerMigration008Comments(ctx context.Context, conn *sql.Conn) error {
	for _, table := range []string{"group_join_requests", "scheduled_jobs"} {
		clauses := make([]string, 0, len(managerMigration008CommentSpecs))
		for _, spec := range managerMigration008CommentSpecs {
			if spec.table != table {
				continue
			}
			clauses = append(clauses, fmt.Sprintf(
				"MODIFY COLUMN `%s` %s COMMENT '%s'",
				spec.column, spec.definition, strings.ReplaceAll(spec.canonical, "'", "''"),
			))
		}
		if _, err := conn.ExecContext(ctx, "ALTER TABLE `"+table+"` "+strings.Join(clauses, ", ")); err != nil {
			return safeDatabaseError("normalize migration 008 "+table+" comments", err)
		}
	}
	return nil
}

func sameNullableString(actual sql.NullString, expected *string) bool {
	if expected == nil {
		return !actual.Valid
	}
	return actual.Valid && actual.String == *expected
}

func doubleEncodedUTF8(value string) string {
	bytes := []byte(value)
	runes := make([]rune, 0, len(bytes))
	for _, value := range bytes {
		if replacement, ok := migrationWindows1252Runes[value]; ok {
			runes = append(runes, replacement)
		} else {
			runes = append(runes, rune(value))
		}
	}
	return string(runes)
}

var migrationWindows1252Runes = map[byte]rune{
	0x80: '\u20ac', 0x82: '\u201a', 0x83: '\u0192', 0x84: '\u201e', 0x85: '\u2026', 0x86: '\u2020', 0x87: '\u2021',
	0x88: '\u02c6', 0x89: '\u2030', 0x8a: '\u0160', 0x8b: '\u2039', 0x8c: '\u0152', 0x8e: '\u017d',
	0x91: '\u2018', 0x92: '\u2019', 0x93: '\u201c', 0x94: '\u201d', 0x95: '\u2022', 0x96: '\u2013', 0x97: '\u2014',
	0x98: '\u02dc', 0x99: '\u2122', 0x9a: '\u0161', 0x9b: '\u203a', 0x9c: '\u0153', 0x9e: '\u017e', 0x9f: '\u0178',
}

func stringPointer(value string) *string {
	return &value
}
