ALTER TABLE group_join_requests
  ADD COLUMN automatic_review JSON NULL AFTER validation_snapshot;

ALTER TABLE group_join_decisions
  ADD COLUMN review_snapshot JSON NULL AFTER validation_snapshot;

CREATE TABLE join_approval_rule_state (
  rule_version INT UNSIGNED NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evidence_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
  activated_at DATETIME(3) NULL,
  rebuilt_at DATETIME(3) NULL,
  last_error_code VARCHAR(100) NULL,
  revision INT UNSIGNED NOT NULL DEFAULT 1,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (rule_version),
  CONSTRAINT chk_join_approval_rule_state_status CHECK (status IN ('building', 'ready', 'failed')),
  CONSTRAINT chk_join_approval_rule_state_revision CHECK (revision >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE join_major_code_samples (
  sample_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  enrollment_year CHAR(4) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  major_code CHAR(3) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  major_name VARCHAR(128) NOT NULL,
  normalized_major VARCHAR(128) NOT NULL,
  source_request_id BIGINT UNSIGNED NOT NULL,
  source_decision_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  approval_source VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_group_id BIGINT NOT NULL,
  active TINYINT(1) NOT NULL DEFAULT 1,
  revision INT UNSIGNED NOT NULL DEFAULT 1,
  corrected_by_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  corrected_by_user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  corrected_by_display_name VARCHAR(100) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (sample_id),
  UNIQUE KEY uq_join_major_code_samples_request (source_request_id),
  KEY idx_join_major_code_samples_lookup (enrollment_year, major_code, active, normalized_major),
  KEY idx_join_major_code_samples_decision (source_decision_id),
  CONSTRAINT fk_join_major_code_samples_request FOREIGN KEY (source_request_id) REFERENCES group_join_requests (id),
  CONSTRAINT fk_join_major_code_samples_decision FOREIGN KEY (source_decision_id) REFERENCES group_join_decisions (decision_id),
  CONSTRAINT chk_join_major_code_samples_source CHECK (approval_source IN ('manual', 'automatic')),
  CONSTRAINT chk_join_major_code_samples_revision CHECK (revision >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE admission_roster_versions (
  version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  content_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  file_name VARCHAR(255) NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  row_count INT UNSIGNED NOT NULL DEFAULT 0,
  revision INT UNSIGNED NOT NULL DEFAULT 1,
  imported_by_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  imported_by_user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  imported_by_display_name VARCHAR(100) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  activated_at DATETIME(3) NULL,
  active_key TINYINT GENERATED ALWAYS AS (CASE WHEN status = 'active' THEN 1 ELSE NULL END) STORED,
  PRIMARY KEY (version_id),
  UNIQUE KEY uq_admission_roster_versions_idempotency (idempotency_key),
  UNIQUE KEY uq_admission_roster_versions_active (active_key),
  KEY idx_admission_roster_versions_created (created_at, version_id),
  CONSTRAINT chk_admission_roster_versions_status CHECK (status IN ('active', 'superseded')),
  CONSTRAINT chk_admission_roster_versions_revision CHECK (revision >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE join_evidence_rebuild_operations (
  actor_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  result_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (actor_id, idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE admission_roster_entries (
  version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  student_id CHAR(12) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  student_name VARCHAR(64) NULL,
  major VARCHAR(128) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version_id, student_id),
  KEY idx_admission_roster_entries_student (student_id, version_id),
  CONSTRAINT fk_admission_roster_entries_version FOREIGN KEY (version_id) REFERENCES admission_roster_versions (version_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO join_approval_rule_state
  (rule_version, status, evidence_version, activated_at, rebuilt_at, last_error_code, revision)
VALUES (2, 'building', 0, NULL, NULL, NULL, 1);
