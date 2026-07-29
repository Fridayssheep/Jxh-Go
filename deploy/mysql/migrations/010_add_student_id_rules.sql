-- Add the versioned global student ID assessment configuration.
CREATE TABLE `student_id_rules` (
  `rule_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `config_json` json NOT NULL,
  `revision` int unsigned NOT NULL DEFAULT 1,
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`rule_id`),
  CONSTRAINT `chk_student_id_rules_config` CHECK (JSON_TYPE(`config_json`) = 'OBJECT'),
  CONSTRAINT `chk_student_id_rules_revision` CHECK (`revision` >= 1),
  CONSTRAINT `chk_student_id_rules_actor_type` CHECK (`updated_by_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_student_id_rules_actor_role` CHECK (`updated_by_role` IS NULL OR `updated_by_role` IN ('super_admin', 'maintainer', 'observer'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO `student_id_rules`
  (`rule_id`, `config_json`, `revision`, `updated_by_type`, `updated_by_user_id`, `updated_by_qq_user_id`, `updated_by_display_name`, `updated_by_role`)
VALUES
  ('student_id_rule', JSON_OBJECT('enabled', FALSE, 'student_id_length', 12, 'enrollment_year_segment', NULL, 'major_code_segment', NULL, 'mappings', JSON_ARRAY()), 1, 'system', NULL, NULL, 'system', NULL);
