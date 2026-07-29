-- Jxh Manager final MySQL schema.
-- This standalone initializer applies the immutable migration chain to an empty database.

SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- 001_create_core_schema
-- 精小弘 Go bot MySQL schema
-- 运行时不使用 AutoMigrate；MySQL 8.4 首次初始化时执行本文件。

CREATE TABLE IF NOT EXISTS `knowledge_trigger_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `source_key` varchar(255) NOT NULL COMMENT 'WPS 词条稳定键',
  `trigger_type` varchar(32) NOT NULL COMMENT 'keyword_reply 或 ai_retrieval',
  `group_id` bigint NOT NULL COMMENT '触发所在 QQ 群',
  `triggered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_trigger_stats` (`triggered_at`, `source_key`, `trigger_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `scheduled_jobs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `type` varchar(16) NOT NULL COMMENT '任务类型：每天/单次',
  `time_hhmm` varchar(5) NOT NULL COMMENT '触发时间，格式 HH:MM',
  `group_id` bigint NOT NULL COMMENT 'QQ群号',
  `message` text NOT NULL COMMENT '定时发送内容',
  `enabled` boolean NOT NULL COMMENT '是否启用',
  `last_run_at` datetime(3) DEFAULT NULL COMMENT '最近执行时间；用于防止同一天重复触发',
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `group_join_requests` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `request_key` varchar(191) NOT NULL COMMENT '群申请业务去重键',
  `flag` varchar(512) DEFAULT NULL COMMENT '处理群申请时需要的 flag',
  `group_id` bigint DEFAULT NULL COMMENT 'QQ群号',
  `user_id` bigint DEFAULT NULL COMMENT '申请人 QQ',
  `student_id` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的学号',
  `student_name` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的姓名',
  `sub_type` varchar(32) DEFAULT NULL COMMENT '申请类型：add/invite 等',
  `comment` text DEFAULT NULL COMMENT '申请验证信息',
  `status` varchar(32) NOT NULL COMMENT '登记状态：pending/observed 等',
  `source` varchar(32) NOT NULL COMMENT '来源：event/system',
  `raw_json` mediumtext DEFAULT NULL COMMENT 'NapCat 原始事件或系统消息 JSON',
  `requested_at` datetime(3) DEFAULT NULL COMMENT '申请时间',
  `first_seen_at` datetime(3) DEFAULT NULL COMMENT '首次登记时间',
  `last_seen_at` datetime(3) DEFAULT NULL COMMENT '最近出现时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_group_join_requests_request_key` (`request_key`),
  KEY `idx_group_join_requests_group_id` (`group_id`),
  KEY `idx_group_join_requests_user_id` (`user_id`),
  KEY `idx_group_join_requests_status` (`status`),
  KEY `idx_group_join_requests_last_seen_at` (`last_seen_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 002_add_run_date_to_scheduled_jobs
ALTER TABLE `scheduled_jobs`
ADD COLUMN `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL'
AFTER `time_hhmm`;

UPDATE `scheduled_jobs`
SET `run_date` = CURRENT_DATE
WHERE `type` = '单次' AND `run_date` IS NULL AND `enabled` = TRUE;

-- 003_expand_group_request_flag
-- 没有 flag 的旧记录无法对应 NapCat 群通知，不能参与后续去重或处理。
DELETE FROM `group_join_requests`
WHERE `flag` IS NULL OR `flag` = '';

ALTER TABLE `group_join_requests`
  DROP INDEX `idx_group_join_requests_request_key`,
  MODIFY COLUMN `flag` varchar(512) NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串',
  ADD UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  DROP COLUMN `request_key`;

-- 004_use_binary_collation_for_identifiers
ALTER TABLE `group_join_requests`
  MODIFY COLUMN `flag` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串';

SET @has_trigger_log_table = (
  SELECT COUNT(*)
  FROM `information_schema`.`tables`
  WHERE `table_schema` = DATABASE()
    AND `table_name` = 'knowledge_trigger_logs'
);
SET @alter_trigger_source_key = IF(
  @has_trigger_log_table > 0,
  'ALTER TABLE `knowledge_trigger_logs` MODIFY COLUMN `source_key` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT ''WPS 词条稳定键''',
  'SELECT 1'
);
PREPARE alter_trigger_source_key_stmt FROM @alter_trigger_source_key;
EXECUTE alter_trigger_source_key_stmt;
DEALLOCATE PREPARE alter_trigger_source_key_stmt;

-- 005_automate_group_request_processing
ALTER TABLE `group_join_requests`
  ADD COLUMN `system_request_id` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL COMMENT 'NapCat 系统消息 request_id' AFTER `flag`,
  ADD COLUMN `major` varchar(128) DEFAULT NULL COMMENT 'AI 从验证信息中提取的专业' AFTER `student_name`,
  ADD COLUMN `system_raw_json` mediumtext DEFAULT NULL COMMENT 'NapCat 最近一次系统消息 JSON' AFTER `raw_json`,
  ADD COLUMN `ai_parse_status` varchar(32) NOT NULL DEFAULT 'skipped' COMMENT 'AI 解析状态：pending/completed/failed/skipped' AFTER `system_raw_json`,
  ADD COLUMN `ai_parse_attempts` int unsigned NOT NULL DEFAULT 0 COMMENT 'AI 解析尝试次数' AFTER `ai_parse_status`,
  ADD COLUMN `processed_at` datetime(3) DEFAULT NULL COMMENT '首次观察到已处理状态的时间' AFTER `requested_at`,
  ADD COLUMN `ai_parsed_at` datetime(3) DEFAULT NULL COMMENT 'AI 解析完成时间' AFTER `last_seen_at`,
  ADD UNIQUE KEY `idx_group_join_requests_system_request_id` (`system_request_id`),
  ADD KEY `idx_group_join_requests_ai_parse_status` (`ai_parse_status`);

UPDATE `group_join_requests`
SET `status` = 'processed',
    `processed_at` = COALESCE(`processed_at`, `last_seen_at`, `first_seen_at`, `requested_at`)
WHERE `status` = 'observed';

ALTER TABLE `group_join_requests`
  ALTER COLUMN `ai_parse_status` SET DEFAULT 'pending';

-- 006_reparse_group_request_applicants
-- 清理旧解析器从“学院+姓名+学号”等问题模板中误提取的加号。
-- 历史 add 申请中尚未成功解析的记录重新进入 AI 队列；invite 不包含申请人资料，不参与回填。
UPDATE `group_join_requests`
SET `student_name` = CASE
      WHEN TRIM(COALESCE(`student_name`, '')) IN ('+', '＋') THEN NULL
      ELSE `student_name`
    END,
    `major` = CASE
      WHEN TRIM(COALESCE(`major`, '')) IN ('+', '＋') THEN NULL
      ELSE `major`
    END,
    `ai_parse_status` = 'pending',
    `ai_parse_attempts` = 0,
    `ai_parsed_at` = NULL
WHERE `sub_type` = 'add'
  AND (
    `ai_parse_status` <> 'completed'
    OR TRIM(COALESCE(`student_name`, '')) IN ('+', '＋')
    OR TRIM(COALESCE(`major`, '')) IN ('+', '＋')
  );

-- 007_remove_group_request_system_request_id
-- system_request_id and flag represented the same NapCat identifier in two APIs.
-- A restart sees either the exact legacy pair or the exact completed absence; mixed states fail closed.
DROP PROCEDURE IF EXISTS `jxh_guard_007`;
DELIMITER $$
CREATE PROCEDURE `jxh_guard_007`()
BEGIN
  DECLARE `table_count` int DEFAULT 0;
  DECLARE `exact_table_count` int DEFAULT 0;
  DECLARE `column_count` int DEFAULT 0;
  DECLARE `exact_column_count` int DEFAULT 0;
  DECLARE `index_part_count` int DEFAULT 0;
  DECLARE `exact_index_part_count` int DEFAULT 0;
  DECLARE `constraint_count` int DEFAULT 0;

  SELECT COUNT(*), COALESCE(SUM(
    BINARY `table_type` = BINARY 'BASE TABLE'
    AND BINARY `engine` = BINARY 'InnoDB'
    AND BINARY `table_collation` = BINARY 'utf8mb4_0900_ai_ci'
  ), 0)
  INTO `table_count`, `exact_table_count`
  FROM information_schema.tables
  WHERE `table_schema` = DATABASE()
    AND BINARY `table_name` = BINARY 'group_join_requests';

  IF `table_count` <> 1 OR `exact_table_count` <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'group_join_requests table is missing or incompatible';
  END IF;

  SELECT COUNT(*), COALESCE(SUM(
    `ordinal_position` = 3
    AND BINARY `column_type` = BINARY 'varchar(64)'
    AND BINARY `is_nullable` = BINARY 'YES'
    AND `column_default` IS NULL
    AND BINARY `collation_name` = BINARY 'utf8mb4_bin'
    AND BINARY `extra` = BINARY ''
  ), 0)
  INTO `column_count`, `exact_column_count`
  FROM information_schema.columns
  WHERE `table_schema` = DATABASE()
    AND BINARY `table_name` = BINARY 'group_join_requests'
    AND BINARY `column_name` = BINARY 'system_request_id';

  SELECT COUNT(*), COALESCE(SUM(
    `non_unique` = 0
    AND `seq_in_index` = 1
    AND BINARY `column_name` = BINARY 'system_request_id'
    AND `sub_part` IS NULL
    AND BINARY `index_type` = BINARY 'BTREE'
    AND BINARY `is_visible` = BINARY 'YES'
    AND `expression` IS NULL
  ), 0)
  INTO `index_part_count`, `exact_index_part_count`
  FROM information_schema.statistics
  WHERE `table_schema` = DATABASE()
    AND BINARY `table_name` = BINARY 'group_join_requests'
    AND BINARY `index_name` = BINARY 'idx_group_join_requests_system_request_id';

  SELECT COUNT(*)
  INTO `constraint_count`
  FROM information_schema.table_constraints
  WHERE `constraint_schema` = DATABASE()
    AND BINARY `table_name` = BINARY 'group_join_requests'
    AND BINARY `constraint_name` = BINARY 'idx_group_join_requests_system_request_id'
    AND BINARY `constraint_type` = BINARY 'UNIQUE';

  IF `column_count` = 0 AND `index_part_count` = 0 AND `constraint_count` = 0 THEN
    BEGIN END;
  ELSEIF `column_count` = 1 AND `exact_column_count` = 1
      AND `index_part_count` = 1 AND `exact_index_part_count` = 1
      AND `constraint_count` = 1 THEN
    IF EXISTS (
      SELECT 1
      FROM `group_join_requests` AS `request`
      WHERE `request`.`flag` IS NULL
         OR `request`.`flag` = ''
         OR (`request`.`system_request_id` IS NOT NULL AND (
              `request`.`system_request_id` = ''
              OR BINARY `request`.`system_request_id` <> BINARY `request`.`flag`
              OR EXISTS (
                SELECT 1
                FROM `group_join_requests` AS `other`
                WHERE `other`.`id` <> `request`.`id`
                  AND BINARY `other`.`flag` = BINARY `request`.`system_request_id`
              )
            ))
    ) OR EXISTS (
      SELECT 1
      FROM `group_join_requests`
      GROUP BY BINARY `flag`
      HAVING COUNT(*) > 1
    ) OR EXISTS (
      SELECT 1
      FROM `group_join_requests`
      WHERE `system_request_id` IS NOT NULL
      GROUP BY BINARY `system_request_id`
      HAVING COUNT(*) > 1
    ) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'group request identifiers are inconsistent';
    END IF;

    ALTER TABLE `group_join_requests`
      DROP INDEX `idx_group_join_requests_system_request_id`,
      DROP COLUMN `system_request_id`;
  ELSE
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'system_request_id state is inconsistent';
  END IF;
END$$
DELIMITER ;
CALL `jxh_guard_007`();
DROP PROCEDURE `jxh_guard_007`;

-- 008_create_manager_schema
DROP PROCEDURE IF EXISTS `jxh_guard_008`;
DELIMITER $$
CREATE PROCEDURE `jxh_guard_008`()
BEGIN
  IF EXISTS (
    SELECT 1 FROM `group_join_requests`
    WHERE BINARY `status` NOT IN (BINARY 'pending', BINARY 'processed')
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'unknown legacy group request status';
  END IF;
  IF EXISTS (
    SELECT 1 FROM `group_join_requests`
    WHERE `sub_type` IS NULL OR BINARY `sub_type` NOT IN (BINARY 'add', BINARY 'invite')
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'unknown legacy group request sub type';
  END IF;
  IF EXISTS (
    SELECT 1 FROM `group_join_requests`
    WHERE BINARY `source` NOT IN (BINARY 'event', BINARY 'system')
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'unknown legacy group request source';
  END IF;
  IF EXISTS (
    SELECT 1 FROM `group_join_requests`
    WHERE BINARY `ai_parse_status` NOT IN (BINARY 'pending', BINARY 'running', BINARY 'completed', BINARY 'succeeded', BINARY 'failed', BINARY 'skipped')
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'unknown legacy group request AI parse status';
  END IF;
  IF EXISTS (
    SELECT 1 FROM `scheduled_jobs`
    WHERE BINARY `type` NOT IN (BINARY '每天', BINARY '单次')
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'unknown legacy scheduled job type';
  END IF;
  IF EXISTS (
    SELECT 1 FROM `scheduled_jobs`
    WHERE `enabled` NOT IN (FALSE, TRUE)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'invalid legacy scheduled job enabled flag';
  END IF;
END$$
DELIMITER ;
CALL `jxh_guard_008`();
DROP PROCEDURE `jxh_guard_008`;

SET SESSION group_concat_max_len = 1048576;

DROP PROCEDURE IF EXISTS `jxh_assert_table_008`;
DELIMITER $$
CREATE PROCEDURE `jxh_assert_table_008`(IN `expected_table` varchar(64), IN `expected_fingerprint` char(64))
BEGIN
  DECLARE `actual_fingerprint` char(64);
  DECLARE `structure_error` varchar(128);

  SELECT SHA2(CONCAT(
    'table:', COALESCE((
      SELECT CONCAT_WS(':',
        CONCAT('V', HEX(`table_type`)),
        IF(`engine` IS NULL, 'N', CONCAT('V', HEX(`engine`))),
        IF(`table_collation` IS NULL, 'N', CONCAT('V', HEX(`table_collation`)))
      )
      FROM information_schema.tables
      WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), '!missing'),
    '|columns:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`column_name`)), `ordinal_position`,
        CONCAT('V', HEX(`column_type`)), CONCAT('V', HEX(`is_nullable`)),
        IF(`column_default` IS NULL, 'N', CONCAT('V', HEX(`column_default`))),
        IF(`character_set_name` IS NULL, 'N', CONCAT('V', HEX(`character_set_name`))),
        IF(`collation_name` IS NULL, 'N', CONCAT('V', HEX(`collation_name`))),
        CONCAT('V', HEX(`extra`)), CONCAT('V', HEX(`generation_expression`)),
        CONCAT('V', HEX(`column_comment`))
      ) ORDER BY `ordinal_position` SEPARATOR '|')
      FROM information_schema.columns
      WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), ''),
    '|indexes:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`index_name`)), `non_unique`, `seq_in_index`,
        IF(`column_name` IS NULL, 'N', CONCAT('V', HEX(`column_name`))),
        IF(`collation` IS NULL, 'N', CONCAT('V', HEX(`collation`))),
        IF(`sub_part` IS NULL, 'N', CONCAT('V', `sub_part`)),
        CONCAT('V', HEX(`nullable`)), CONCAT('V', HEX(`index_type`)),
        CONCAT('V', HEX(`comment`)), CONCAT('V', HEX(`index_comment`)),
        CONCAT('V', HEX(`is_visible`)),
        IF(`expression` IS NULL, 'N', CONCAT('V', HEX(`expression`)))
      ) ORDER BY `index_name`, `seq_in_index` SEPARATOR '|')
      FROM information_schema.statistics
      WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), ''),
    '|constraints:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`constraint_name`)), CONCAT('V', HEX(`constraint_type`)),
        CONCAT('V', HEX(`enforced`))
      ) ORDER BY `constraint_name` SEPARATOR '|')
      FROM information_schema.table_constraints
      WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), ''),
    '|keys:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`constraint_name`)), `ordinal_position`,
        IF(`position_in_unique_constraint` IS NULL, 'N', CONCAT('V', `position_in_unique_constraint`)),
        CONCAT('V', HEX(`column_name`)),
        IF(`referenced_table_name` IS NULL, 'N', CONCAT('V', HEX(`referenced_table_name`))),
        IF(`referenced_column_name` IS NULL, 'N', CONCAT('V', HEX(`referenced_column_name`)))
      ) ORDER BY `constraint_name`, `ordinal_position` SEPARATOR '|')
      FROM information_schema.key_column_usage
      WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), ''),
    '|references:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`constraint_name`)), CONCAT('V', HEX(`unique_constraint_name`)),
        CONCAT('V', HEX(`match_option`)), CONCAT('V', HEX(`update_rule`)),
        CONCAT('V', HEX(`delete_rule`))
      ) ORDER BY `constraint_name` SEPARATOR '|')
      FROM information_schema.referential_constraints
      WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY `expected_table`
    ), ''),
    '|checks:', COALESCE((
      SELECT GROUP_CONCAT(CONCAT_WS(':',
        CONCAT('V', HEX(`tc`.`constraint_name`)), CONCAT('V', HEX(`cc`.`check_clause`))
      ) ORDER BY `tc`.`constraint_name` SEPARATOR '|')
      FROM information_schema.table_constraints AS `tc`
      JOIN information_schema.check_constraints AS `cc`
        ON `cc`.`constraint_schema` = `tc`.`constraint_schema`
       AND `cc`.`constraint_name` = `tc`.`constraint_name`
      WHERE `tc`.`constraint_schema` = DATABASE()
        AND BINARY `tc`.`table_name` = BINARY `expected_table`
        AND BINARY `tc`.`constraint_type` = BINARY 'CHECK'
    ), '')
  ), 256)
  INTO `actual_fingerprint`;

  IF `actual_fingerprint` IS NULL OR BINARY `actual_fingerprint` <> BINARY `expected_fingerprint` THEN
    SET `structure_error` = CONCAT('manager migration structure mismatch: ', `expected_table`, ' ', `actual_fingerprint`);
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = `structure_error`;
  END IF;
END$$
DELIMITER ;

DROP PROCEDURE IF EXISTS `jxh_upgrade_core_008`;
DELIMITER $$
CREATE PROCEDURE `jxh_upgrade_core_008`()
BEGIN
  DECLARE `group_stage` int DEFAULT -1;
  DECLARE `group_columns` int DEFAULT 0;
  DECLARE `group_indexes` int DEFAULT 0;
  DECLARE `group_checks` int DEFAULT 0;
  DECLARE `group_final_fk` int DEFAULT 0;
  DECLARE `scheduled_stage` int DEFAULT -1;
  DECLARE `scheduled_columns` int DEFAULT 0;
  DECLARE `scheduled_indexes` int DEFAULT 0;
  DECLARE `scheduled_checks` int DEFAULT 0;

  SELECT COUNT(*) INTO `group_columns`
  FROM information_schema.columns
  WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_requests'
    AND `column_name` IN ('applicant_nickname', 'ai_error_code', 'validation_snapshot', 'observed_status', 'decision_status', 'decision_source', 'revision', 'last_decision_id', 'processing_expires_at');
  SELECT COUNT(DISTINCT `index_name`) INTO `group_indexes`
  FROM information_schema.statistics
  WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_requests'
    AND `index_name` IN ('idx_group_join_requests_group_decision_cursor', 'idx_group_join_requests_observed_cursor', 'idx_group_join_requests_user_cursor');
  SELECT COUNT(*) INTO `group_checks`
  FROM information_schema.table_constraints
  WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_requests'
    AND `constraint_name` IN ('chk_group_join_requests_legacy_status', 'chk_group_join_requests_sub_type', 'chk_group_join_requests_source', 'chk_group_join_requests_ai_status', 'chk_group_join_requests_observed_status', 'chk_group_join_requests_decision_status', 'chk_group_join_requests_decision_source', 'chk_group_join_requests_revision');
  SELECT COUNT(*) INTO `group_final_fk`
  FROM information_schema.table_constraints
  WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_requests'
    AND BINARY `constraint_name` = BINARY 'fk_group_join_requests_last_decision'
    AND BINARY `constraint_type` = BINARY 'FOREIGN KEY';

  IF `group_columns` = 0 AND `group_indexes` = 0 AND `group_checks` = 0 AND `group_final_fk` = 0 THEN
    CALL `jxh_assert_table_008`('group_join_requests', '9f002b9b9750db23fe232ec498fb258bb70af41f898eef617c9e1c517584a26b');
    SET `group_stage` = 0;
  ELSEIF `group_columns` = 9 AND `group_indexes` = 3 AND `group_checks` = 0 AND `group_final_fk` = 0 THEN
    CALL `jxh_assert_table_008`('group_join_requests', '37e3e23466af441fc35d1982a82065b0c6b15ddf4236efd6e44bc734f7c734f8');
    SET `group_stage` = 1;
  ELSEIF `group_columns` = 9 AND `group_indexes` = 3 AND `group_checks` = 8 AND `group_final_fk` = 0 THEN
    CALL `jxh_assert_table_008`('group_join_requests', 'e022dfa641d79d9eb275cec2fcb9e4b167da90303250525d6d0b85acf8572f32');
    SET `group_stage` = 2;
  ELSEIF `group_columns` = 9 AND `group_indexes` = 3 AND `group_checks` = 8 AND `group_final_fk` = 1 THEN
    CALL `jxh_assert_table_008`('group_join_requests', 'd045995d2be632cd562b5bd0606e3ecafd8159a5d73bfee5ecd1a151bdf8fe02');
    SET `group_stage` = 3;
  ELSE
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'group_join_requests migration state is inconsistent';
  END IF;

  SELECT COUNT(*) INTO `scheduled_columns`
  FROM information_schema.columns
  WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'scheduled_jobs'
    AND `column_name` IN ('name', 'status', 'timezone', 'run_at', 'revision', 'last_run_result', 'updated_by_type', 'updated_by_user_id', 'updated_by_qq_user_id', 'updated_by_display_name', 'updated_by_role', 'archived_at');
  SELECT COUNT(DISTINCT `index_name`) INTO `scheduled_indexes`
  FROM information_schema.statistics
  WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'scheduled_jobs'
    AND `index_name` IN ('idx_scheduled_jobs_group_status_cursor', 'idx_scheduled_jobs_status_run_cursor');
  SELECT COUNT(*) INTO `scheduled_checks`
  FROM information_schema.table_constraints
  WHERE `constraint_schema` = DATABASE() AND BINARY `table_name` = BINARY 'scheduled_jobs'
    AND `constraint_name` IN ('chk_scheduled_jobs_type', 'chk_scheduled_jobs_status', 'chk_scheduled_jobs_last_run_result', 'chk_scheduled_jobs_revision', 'chk_scheduled_jobs_actor_type', 'chk_scheduled_jobs_actor_role', 'chk_scheduled_jobs_actor');

  IF `scheduled_columns` = 0 AND `scheduled_indexes` = 0 AND `scheduled_checks` = 0 THEN
    CALL `jxh_assert_table_008`('scheduled_jobs', 'b05e751d48ef9b375060f8df412d254c630baccfaf6537f2ff6603aaf1968e24');
    SET `scheduled_stage` = 0;
  ELSEIF `scheduled_columns` = 12 AND `scheduled_indexes` = 2 AND `scheduled_checks` = 0 THEN
    CALL `jxh_assert_table_008`('scheduled_jobs', 'b575eeedfdb3addc40d6fb1ce78b6ba8ebc6560237d9143b9a859851b5c53654');
    SET `scheduled_stage` = 1;
  ELSEIF `scheduled_columns` = 12 AND `scheduled_indexes` = 2 AND `scheduled_checks` = 7 THEN
    CALL `jxh_assert_table_008`('scheduled_jobs', 'c26accb90d69ae2f0c5be12674cef5e8c35853c14fc2360eb943246aa8b5caa7');
    SET `scheduled_stage` = 2;
  ELSE
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'scheduled_jobs migration state is inconsistent';
  END IF;

  IF `group_stage` = 0 THEN
    ALTER TABLE `group_join_requests`
      ADD COLUMN `applicant_nickname` varchar(100) DEFAULT NULL AFTER `user_id`,
      ADD COLUMN `ai_error_code` varchar(100) DEFAULT NULL AFTER `ai_parse_attempts`,
      ADD COLUMN `validation_snapshot` json DEFAULT NULL AFTER `ai_error_code`,
      ADD COLUMN `observed_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending' AFTER `status`,
      ADD COLUMN `decision_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending' AFTER `observed_status`,
      ADD COLUMN `decision_source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `decision_status`,
      ADD COLUMN `revision` int unsigned NOT NULL DEFAULT 1 AFTER `decision_source`,
      ADD COLUMN `last_decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `revision`,
      ADD COLUMN `processing_expires_at` datetime(3) DEFAULT NULL AFTER `last_decision_id`,
      ADD KEY `idx_group_join_requests_group_decision_cursor` (`group_id`, `decision_status`, `last_seen_at`, `id`),
      ADD KEY `idx_group_join_requests_observed_cursor` (`observed_status`, `last_seen_at`, `id`),
      ADD KEY `idx_group_join_requests_user_cursor` (`user_id`, `last_seen_at`, `id`);
    -- jxh:008-stage group-columns
    CALL `jxh_assert_table_008`('group_join_requests', '37e3e23466af441fc35d1982a82065b0c6b15ddf4236efd6e44bc734f7c734f8');
  END IF;

  IF `group_stage` < 2 THEN
    UPDATE `group_join_requests`
    SET `observed_status` = CASE WHEN `status` = 'processed' THEN 'checked' ELSE 'pending' END,
        `decision_status` = CASE WHEN `status` = 'processed' THEN 'external_processed' ELSE 'pending' END,
        `decision_source` = CASE WHEN `status` = 'processed' THEN 'external' ELSE NULL END
    WHERE `revision` = 1
      AND `last_decision_id` IS NULL
      AND `processing_expires_at` IS NULL
      AND BINARY `observed_status` IN (BINARY 'pending', BINARY 'checked')
      AND BINARY `decision_status` IN (BINARY 'pending', BINARY 'external_processed')
      AND (`decision_source` IS NULL OR BINARY `decision_source` = BINARY 'external');

    UPDATE `group_join_requests`
    SET `ai_parse_status` = 'succeeded'
    WHERE BINARY `ai_parse_status` = BINARY 'completed';
    -- jxh:008-stage group-backfill

    ALTER TABLE `group_join_requests`
      MODIFY COLUMN `sub_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '申请类型：add/invite 等',
      MODIFY COLUMN `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '登记状态：pending/observed 等',
      MODIFY COLUMN `source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '来源：event/system',
      MODIFY COLUMN `ai_parse_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending' COMMENT 'AI 解析状态：pending/completed/failed/skipped',
      ADD CONSTRAINT `chk_group_join_requests_legacy_status` CHECK (`status` IN ('pending', 'processed')),
      ADD CONSTRAINT `chk_group_join_requests_sub_type` CHECK (`sub_type` IN ('add', 'invite')),
      ADD CONSTRAINT `chk_group_join_requests_source` CHECK (`source` IN ('event', 'system')),
      ADD CONSTRAINT `chk_group_join_requests_ai_status` CHECK (`ai_parse_status` IN ('pending', 'running', 'succeeded', 'failed', 'skipped')),
      ADD CONSTRAINT `chk_group_join_requests_observed_status` CHECK (`observed_status` IN ('pending', 'checked')),
      ADD CONSTRAINT `chk_group_join_requests_decision_status` CHECK (`decision_status` IN ('pending', 'processing', 'approved', 'rejected', 'external_processed', 'unknown')),
      ADD CONSTRAINT `chk_group_join_requests_decision_source` CHECK (`decision_source` IS NULL OR `decision_source` IN ('manual', 'automatic', 'external')),
      ADD CONSTRAINT `chk_group_join_requests_revision` CHECK (`revision` >= 1);
    -- jxh:008-stage group-constraints
    CALL `jxh_assert_table_008`('group_join_requests', 'e022dfa641d79d9eb275cec2fcb9e4b167da90303250525d6d0b85acf8572f32');
  END IF;

  IF `scheduled_stage` = 0 THEN
    ALTER TABLE `scheduled_jobs`
      ADD COLUMN `name` varchar(100) NOT NULL DEFAULT 'Scheduled job' AFTER `id`,
      ADD COLUMN `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active' AFTER `enabled`,
      ADD COLUMN `timezone` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'Asia/Shanghai' AFTER `status`,
      ADD COLUMN `run_at` datetime(3) DEFAULT NULL AFTER `timezone`,
      ADD COLUMN `revision` int unsigned NOT NULL DEFAULT 1 AFTER `last_run_at`,
      ADD COLUMN `last_run_result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `revision`,
      ADD COLUMN `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `last_run_result`,
      ADD COLUMN `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `updated_by_type`,
      ADD COLUMN `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `updated_by_user_id`,
      ADD COLUMN `updated_by_display_name` varchar(100) DEFAULT NULL AFTER `updated_by_qq_user_id`,
      ADD COLUMN `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL AFTER `updated_by_display_name`,
      ADD COLUMN `archived_at` datetime(3) DEFAULT NULL AFTER `updated_at`,
      ADD KEY `idx_scheduled_jobs_group_status_cursor` (`group_id`, `status`, `updated_at`, `id`),
      ADD KEY `idx_scheduled_jobs_status_run_cursor` (`status`, `run_at`, `id`);
    -- jxh:008-stage scheduled-columns
    CALL `jxh_assert_table_008`('scheduled_jobs', 'b575eeedfdb3addc40d6fb1ce78b6ba8ebc6560237d9143b9a859851b5c53654');
  END IF;

  IF `scheduled_stage` < 2 THEN
    UPDATE `scheduled_jobs`
    SET `status` = CASE
          WHEN `enabled` = TRUE THEN 'active'
          WHEN BINARY `type` = BINARY '单次' AND `last_run_at` IS NOT NULL THEN 'completed'
          ELSE 'archived'
        END
    WHERE `revision` = 1 AND BINARY `status` = BINARY 'active';

    UPDATE `scheduled_jobs`
    SET `updated_by_type` = 'system',
        `updated_by_user_id` = NULL,
        `updated_by_qq_user_id` = NULL,
        `updated_by_display_name` = 'system'
    WHERE `updated_by_type` IS NULL;

    UPDATE `scheduled_jobs`
    SET `created_at` = COALESCE(`created_at`, `updated_at`, CURRENT_TIMESTAMP(3)),
        `updated_at` = COALESCE(`updated_at`, `created_at`, CURRENT_TIMESTAMP(3))
    WHERE `created_at` IS NULL OR `updated_at` IS NULL;
    -- jxh:008-stage scheduled-backfill

    ALTER TABLE `scheduled_jobs`
      MODIFY COLUMN `type` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '任务类型：每天/单次',
      MODIFY COLUMN `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
      MODIFY COLUMN `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
      MODIFY COLUMN `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
      MODIFY COLUMN `updated_by_display_name` varchar(100) NOT NULL,
      ADD CONSTRAINT `chk_scheduled_jobs_type` CHECK (`type` IN ('每天', '单次')),
      ADD CONSTRAINT `chk_scheduled_jobs_status` CHECK (`status` IN ('active', 'paused', 'completed', 'archived')),
      ADD CONSTRAINT `chk_scheduled_jobs_last_run_result` CHECK (`last_run_result` IS NULL OR `last_run_result` IN ('success', 'failed', 'unknown', 'skipped')),
      ADD CONSTRAINT `chk_scheduled_jobs_revision` CHECK (`revision` >= 1),
      ADD CONSTRAINT `chk_scheduled_jobs_actor_type` CHECK (`updated_by_type` IN ('admin_user', 'qq_user', 'system')),
      ADD CONSTRAINT `chk_scheduled_jobs_actor_role` CHECK (`updated_by_role` IS NULL OR `updated_by_role` IN ('super_admin', 'maintainer', 'observer')),
      ADD CONSTRAINT `chk_scheduled_jobs_actor` CHECK ((`updated_by_type` = 'system' AND `updated_by_user_id` IS NULL AND `updated_by_qq_user_id` IS NULL) OR (`updated_by_type` = 'admin_user' AND `updated_by_user_id` IS NOT NULL) OR (`updated_by_type` = 'qq_user' AND `updated_by_qq_user_id` IS NOT NULL));
    -- jxh:008-stage scheduled-constraints
    CALL `jxh_assert_table_008`('scheduled_jobs', 'c26accb90d69ae2f0c5be12674cef5e8c35853c14fc2360eb943246aa8b5caa7');
  END IF;
END$$
DELIMITER ;
CALL `jxh_upgrade_core_008`();
DROP PROCEDURE `jxh_upgrade_core_008`;

DROP PROCEDURE IF EXISTS `jxh_create_manager_tables_008`;
DELIMITER $$
CREATE PROCEDURE `jxh_create_manager_tables_008`()
BEGIN
IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'admin_users') THEN
CREATE TABLE `admin_users` (
  `user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `username` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `display_name` varchar(64) NOT NULL,
  `password_hash` varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `enabled` boolean NOT NULL DEFAULT TRUE,
  `last_login_at` datetime(3) DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT 1,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`user_id`),
  UNIQUE KEY `uq_admin_users_username` (`username`),
  UNIQUE KEY `uq_admin_users_qq_user_id` (`qq_user_id`),
  KEY `idx_admin_users_role_enabled_cursor` (`role`, `enabled`, `updated_at`, `user_id`),
  KEY `idx_admin_users_enabled_cursor` (`enabled`, `updated_at`, `user_id`),
  CONSTRAINT `chk_admin_users_role` CHECK (`role` IN ('super_admin', 'maintainer', 'observer')),
  CONSTRAINT `chk_admin_users_revision` CHECK (`revision` >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('admin_users', 'd0446f6cdb5e821260d1fa20ee3972a833ea866710d453945b2e2491d4bc5eb7');
-- jxh:008-stage table-admin_users

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'admin_sessions') THEN
CREATE TABLE `admin_sessions` (
  `session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `token_digest` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `csrf_digest` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
  `ip_address` varchar(64) DEFAULT NULL,
  `user_agent` varchar(300) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_seen_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `expires_at` datetime(3) NOT NULL,
  `absolute_expires_at` datetime(3) NOT NULL,
  `revoked_at` datetime(3) DEFAULT NULL,
  `revoked_reason` varchar(100) DEFAULT NULL,
  `replacement_depth` int unsigned NOT NULL DEFAULT 0,
  `replaced_by_session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `replaced_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `replaced_by_depth` int unsigned DEFAULT NULL,
  PRIMARY KEY (`session_id`),
  UNIQUE KEY `uq_admin_sessions_session_user` (`session_id`, `user_id`),
  UNIQUE KEY `uq_admin_sessions_replacement_target` (`session_id`, `user_id`, `replacement_depth`),
  UNIQUE KEY `uq_admin_sessions_token_digest` (`token_digest`),
  KEY `idx_admin_sessions_user_cursor` (`user_id`, `status`, `last_seen_at`, `session_id`),
  KEY `idx_admin_sessions_expiry` (`status`, `expires_at`, `session_id`),
  KEY `idx_admin_sessions_absolute_expiry` (`absolute_expires_at`, `session_id`),
  KEY `idx_admin_sessions_replacement` (`replaced_by_session_id`, `replaced_by_user_id`, `replaced_by_depth`),
  CONSTRAINT `fk_admin_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `admin_users` (`user_id`),
  CONSTRAINT `fk_admin_sessions_replacement` FOREIGN KEY (`replaced_by_session_id`, `replaced_by_user_id`, `replaced_by_depth`) REFERENCES `admin_sessions` (`session_id`, `user_id`, `replacement_depth`) ON DELETE SET NULL,
  CONSTRAINT `chk_admin_sessions_status` CHECK (`status` IN ('active', 'expired', 'revoked')),
  CONSTRAINT `chk_admin_sessions_revocation` CHECK ((`status` = 'revoked' AND `revoked_at` IS NOT NULL) OR (`status` IN ('active', 'expired') AND `revoked_at` IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('admin_sessions', '950dbcc1abc00132a9fb5c64d34cc18845512e352fa5d1bf72bf39c03b6ae321');
-- jxh:008-stage table-admin_sessions

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'admin_audit_logs') THEN
CREATE TABLE `admin_audit_logs` (
  `audit_log_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `actor_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_display_name` varchar(100) DEFAULT NULL,
  `actor_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `scope_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `scope_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `action` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `target_type` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `target_id` varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL,
  `target_display_name` varchar(200) DEFAULT NULL,
  `result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `ip_address` varchar(64) DEFAULT NULL,
  `user_agent` varchar(300) DEFAULT NULL,
  `before_snapshot` json DEFAULT NULL,
  `after_snapshot` json DEFAULT NULL,
  `metadata` json NOT NULL,
  `redacted` boolean NOT NULL DEFAULT TRUE,
  PRIMARY KEY (`audit_log_id`),
  KEY `idx_admin_audit_time_cursor` (`occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_actor_cursor` (`actor_user_id`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_actor_role_cursor` (`actor_role`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_scope_cursor` (`scope_type`, `scope_id`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_action_cursor` (`action`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_target_cursor` (`target_type`, `target_id`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_result_cursor` (`result`, `occurred_at`, `audit_log_id`),
  KEY `idx_admin_audit_request` (`request_id`),
  CONSTRAINT `chk_admin_audit_actor_type` CHECK (`actor_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_admin_audit_actor_role` CHECK (`actor_role` IS NULL OR `actor_role` IN ('super_admin', 'maintainer', 'observer')),
  CONSTRAINT `chk_admin_audit_result` CHECK (`result` IN ('success', 'failed', 'unknown')),
  CONSTRAINT `chk_admin_audit_source` CHECK (`source` IN ('web', 'qq', 'system'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('admin_audit_logs', 'acc739b182983d23bf28fcf317dd38f83445d5c48505dd6f39ac1ef3568eabda');
-- jxh:008-stage table-admin_audit_logs

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'admin_idempotency_keys') THEN
CREATE TABLE `admin_idempotency_keys` (
  `idempotency_id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `actor_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `operation` varchar(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `request_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `state` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `response_status` smallint unsigned DEFAULT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  `resource_type` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `resource_id` varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL,
  `resulting_session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `trace_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  `expires_at` datetime(3) NOT NULL,
  PRIMARY KEY (`idempotency_id`),
  UNIQUE KEY `uq_admin_idempotency_actor_operation_key` (`actor_type`, `actor_id`, `operation`, `idempotency_key`),
  KEY `idx_admin_idempotency_expiry` (`expires_at`, `idempotency_id`),
  KEY `idx_admin_idempotency_resource` (`resource_type`, `resource_id`, `created_at`, `idempotency_id`),
  KEY `idx_admin_idempotency_session` (`resulting_session_id`),
  CONSTRAINT `fk_admin_idempotency_session` FOREIGN KEY (`resulting_session_id`) REFERENCES `admin_sessions` (`session_id`) ON DELETE SET NULL,
  CONSTRAINT `chk_admin_idempotency_actor_type` CHECK (`actor_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_admin_idempotency_response_status` CHECK (`response_status` IS NULL OR (`response_status` BETWEEN 100 AND 599)),
  CONSTRAINT `chk_admin_idempotency_state` CHECK (`state` IN ('in_progress', 'completed')),
  CONSTRAINT `chk_admin_idempotency_result_status` CHECK (`result_status` IS NULL OR `result_status` IN ('succeeded', 'failed', 'unknown')),
  CONSTRAINT `chk_admin_idempotency_completion` CHECK ((`state` = 'in_progress' AND `result_status` IS NULL AND `completed_at` IS NULL) OR (`state` = 'completed' AND `result_status` IS NOT NULL AND `completed_at` IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('admin_idempotency_keys', '265d7dbce96b4acd7a9cec8473d45b61bcf25116c3d4123bcca5f481e4c2a60e');
-- jxh:008-stage table-admin_idempotency_keys

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'managed_groups') THEN
CREATE TABLE `managed_groups` (
  `group_id` bigint NOT NULL,
  `name` varchar(100) NOT NULL,
  `member_count` int unsigned NOT NULL DEFAULT 0,
  `max_member_count` int unsigned NOT NULL DEFAULT 0,
  `bot_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `snapshot_state` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `last_error_code` varchar(100) DEFAULT NULL,
  `last_error_message` varchar(300) DEFAULT NULL,
  `last_synced_at` datetime(3) DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT 1,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `archived_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`group_id`),
  KEY `idx_managed_groups_state_cursor` (`snapshot_state`, `updated_at`, `group_id`),
  KEY `idx_managed_groups_archived_cursor` (`archived_at`, `updated_at`, `group_id`),
  CONSTRAINT `chk_managed_groups_bot_role` CHECK (`bot_role` IN ('owner', 'admin', 'member', 'unknown')),
  CONSTRAINT `chk_managed_groups_snapshot_state` CHECK (`snapshot_state` IN ('fresh', 'stale')),
  CONSTRAINT `chk_managed_groups_revision` CHECK (`revision` >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('managed_groups', '0442c071de15f54f8af2e84bb20bf98563e95bbe22eb92a17d90fea8b914e9f9');
-- jxh:008-stage table-managed_groups

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'feature_settings') THEN
CREATE TABLE `feature_settings` (
  `setting_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `scope_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint DEFAULT NULL,
  `scope_key` bigint AS (COALESCE(`group_id`, 0)) STORED,
  `settings_json` json NOT NULL,
  `revision` int unsigned NOT NULL DEFAULT 1,
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`setting_id`),
  UNIQUE KEY `uq_feature_settings_scope` (`scope_type`, `scope_key`),
  KEY `idx_feature_settings_group_cursor` (`group_id`, `updated_at`, `setting_id`),
  KEY `idx_feature_settings_updated_cursor` (`updated_at`, `setting_id`),
  CONSTRAINT `chk_feature_settings_scope` CHECK ((`scope_type` = 'global' AND `group_id` IS NULL) OR (`scope_type` = 'group' AND `group_id` IS NOT NULL AND `group_id` > 0)),
  CONSTRAINT `chk_feature_settings_json` CHECK (JSON_TYPE(`settings_json`) = 'OBJECT'),
  CONSTRAINT `chk_feature_settings_revision` CHECK (`revision` >= 1),
  CONSTRAINT `chk_feature_settings_actor_type` CHECK (`updated_by_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_feature_settings_actor_role` CHECK (`updated_by_role` IS NULL OR `updated_by_role` IN ('super_admin', 'maintainer', 'observer'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('feature_settings', 'e51a1ef70a7b1f37641ab728c0746f27be74bbdd36fab26912c566b5c63cbf1c');
-- jxh:008-stage table-feature_settings

INSERT INTO `feature_settings`
  (`setting_id`, `scope_type`, `group_id`, `settings_json`, `revision`, `updated_by_type`, `updated_by_user_id`, `updated_by_qq_user_id`, `updated_by_display_name`, `updated_by_role`)
SELECT 'settings_global', 'global', NULL, JSON_OBJECT(), 1, 'system', NULL, NULL, 'system', NULL
WHERE NOT EXISTS (
  SELECT 1 FROM `feature_settings`
  WHERE BINARY `scope_type` = BINARY 'global' AND `group_id` IS NULL
);
-- jxh:008-stage seed-global-settings

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_policies') THEN
CREATE TABLE `group_join_policies` (
  `group_id` bigint NOT NULL,
  `enabled` boolean NOT NULL DEFAULT FALSE,
  `mode` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'ai_fields_complete',
  `required_fields` json NOT NULL,
  `auto_reject` boolean NOT NULL DEFAULT FALSE,
  `revision` int unsigned NOT NULL DEFAULT 1,
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`group_id`),
  KEY `idx_group_join_policies_enabled_cursor` (`enabled`, `updated_at`, `group_id`),
  CONSTRAINT `chk_group_join_policies_mode` CHECK (`mode` = 'ai_fields_complete'),
  CONSTRAINT `chk_group_join_policies_auto_reject` CHECK (`auto_reject` = FALSE),
  CONSTRAINT `chk_group_join_policies_revision` CHECK (`revision` >= 1),
  CONSTRAINT `chk_group_join_policies_actor_type` CHECK (`updated_by_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_group_join_policies_actor_role` CHECK (`updated_by_role` IS NULL OR `updated_by_role` IN ('super_admin', 'maintainer', 'observer')),
  CONSTRAINT `chk_group_join_policies_fields` CHECK (JSON_TYPE(`required_fields`) = 'ARRAY' AND JSON_LENGTH(`required_fields`) = 3 AND JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[0]')) = 'STRING' AND JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[1]')) = 'STRING' AND JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[2]')) = 'STRING' AND JSON_CONTAINS(`required_fields`, JSON_QUOTE('student_id'), '$') AND JSON_CONTAINS(`required_fields`, JSON_QUOTE('name'), '$') AND JSON_CONTAINS(`required_fields`, JSON_QUOTE('major'), '$'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('group_join_policies', '3db7d6f865728ac1c9b377cbf9f5e1bdf0ba073ca8ec943874b40964b8444398');
-- jxh:008-stage table-group_join_policies

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'custom_commands') THEN
CREATE TABLE `custom_commands` (
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `display_name` varchar(64) NOT NULL,
  `description` varchar(500) NOT NULL DEFAULT '',
  `scope_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `scope_json` json NOT NULL,
  `trigger_permission` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `parameters_json` json NOT NULL,
  `actions_json` json NOT NULL,
  `enabled` boolean NOT NULL DEFAULT FALSE,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'draft',
  `revision` int unsigned NOT NULL DEFAULT 1,
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `archived_at` datetime(3) DEFAULT NULL,
  `active_name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin AS (CASE WHEN `archived_at` IS NULL THEN `name` ELSE NULL END) STORED,
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uq_custom_commands_active_name` (`active_name`),
  KEY `idx_custom_commands_status_cursor` (`status`, `enabled`, `updated_at`, `command_id`),
  KEY `idx_custom_commands_scope_cursor` (`scope_type`, `updated_at`, `command_id`),
  CONSTRAINT `chk_custom_commands_scope` CHECK (`scope_type` IN ('global', 'groups')),
  CONSTRAINT `chk_custom_commands_status` CHECK (`status` IN ('draft', 'active', 'disabled', 'archived')),
  CONSTRAINT `chk_custom_commands_permission` CHECK (`trigger_permission` IN ('everyone', 'group_admin', 'maintenance_allowlist')),
  CONSTRAINT `chk_custom_commands_revision` CHECK (`revision` >= 1),
  CONSTRAINT `chk_custom_commands_actor_type` CHECK (`updated_by_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_custom_commands_actor_role` CHECK (`updated_by_role` IS NULL OR `updated_by_role` IN ('super_admin', 'maintainer', 'observer')),
  CONSTRAINT `chk_custom_commands_actor` CHECK ((`updated_by_type` = 'system' AND `updated_by_user_id` IS NULL AND `updated_by_qq_user_id` IS NULL) OR (`updated_by_type` = 'admin_user' AND `updated_by_user_id` IS NOT NULL) OR (`updated_by_type` = 'qq_user' AND `updated_by_qq_user_id` IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('custom_commands', '2fa144141bdbcfff03fff096bc71fd5cf19383836ddbe498ba273340a8be9ed2');
-- jxh:008-stage table-custom_commands

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'custom_command_runs') THEN
CREATE TABLE `custom_command_runs` (
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `run_identity` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `command_name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint NOT NULL,
  `triggered_by_qq` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `action_steps` json NOT NULL,
  `duration_ms` int unsigned NOT NULL DEFAULT 0,
  `error_code` varchar(100) DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`run_id`),
  UNIQUE KEY `uq_custom_command_runs_identity` (`run_identity`),
  KEY `idx_custom_command_runs_command_cursor` (`command_id`, `occurred_at`, `run_id`),
  KEY `idx_custom_command_runs_group_cursor` (`group_id`, `occurred_at`, `run_id`),
  KEY `idx_custom_command_runs_result_cursor` (`result`, `occurred_at`, `run_id`),
  CONSTRAINT `chk_custom_command_runs_result` CHECK (`result` IN ('success', 'denied', 'parse_error', 'failed', 'partial', 'unknown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('custom_command_runs', '1057e5ba2a1500fa7d5ca9e280e6bd9e1ca64014f0a926fa80fc5eb214bbf64a');
-- jxh:008-stage table-custom_command_runs

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'group_join_decisions') THEN
CREATE TABLE `group_join_decisions` (
  `decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `request_id` bigint unsigned NOT NULL,
  `idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `action` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `reason` varchar(500) DEFAULT NULL,
  `actor_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_display_name` varchar(100) DEFAULT NULL,
  `field_snapshot` json DEFAULT NULL,
  `validation_snapshot` json DEFAULT NULL,
  `rule_version` int unsigned DEFAULT NULL,
  `napcat_result` json DEFAULT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  `trace_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`decision_id`),
  UNIQUE KEY `uq_group_join_decisions_idempotency` (`request_id`, `idempotency_key`),
  UNIQUE KEY `uq_group_join_decisions_request_ref` (`decision_id`, `request_id`),
  KEY `idx_group_join_decisions_request_cursor` (`request_id`, `started_at`, `decision_id`),
  KEY `idx_group_join_decisions_status_cursor` (`status`, `started_at`, `decision_id`),
  KEY `idx_group_join_decisions_actor_cursor` (`actor_user_id`, `started_at`, `decision_id`),
  KEY `idx_group_join_decisions_source_cursor` (`source`, `started_at`, `decision_id`),
  CONSTRAINT `fk_group_join_decisions_request` FOREIGN KEY (`request_id`) REFERENCES `group_join_requests` (`id`),
  CONSTRAINT `chk_group_join_decisions_action` CHECK (`action` IN ('approve', 'reject')),
  CONSTRAINT `chk_group_join_decisions_source` CHECK (`source` IN ('manual', 'automatic', 'external')),
  CONSTRAINT `chk_group_join_decisions_status` CHECK (`status` IN ('started', 'confirmed', 'failed', 'unknown')),
  CONSTRAINT `chk_group_join_decisions_actor_type` CHECK (`actor_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_group_join_decisions_actor_role` CHECK (`actor_role` IS NULL OR `actor_role` IN ('super_admin', 'maintainer', 'observer'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('group_join_decisions', '96d71dc3798cc55e91e47250b7b9a97cbd570f4507264accca40aac1244a482a');
-- jxh:008-stage table-group_join_decisions

IF NOT EXISTS (
  SELECT 1 FROM information_schema.table_constraints
  WHERE `constraint_schema` = DATABASE()
    AND BINARY `table_name` = BINARY 'group_join_requests'
    AND BINARY `constraint_name` = BINARY 'fk_group_join_requests_last_decision'
) THEN
  CALL `jxh_assert_table_008`('group_join_requests', 'e022dfa641d79d9eb275cec2fcb9e4b167da90303250525d6d0b85acf8572f32');
  ALTER TABLE `group_join_requests`
    ADD CONSTRAINT `fk_group_join_requests_last_decision` FOREIGN KEY (`last_decision_id`, `id`) REFERENCES `group_join_decisions` (`decision_id`, `request_id`);
  -- jxh:008-stage group-last-decision-fk
END IF;
CALL `jxh_assert_table_008`('group_join_requests', 'd045995d2be632cd562b5bd0606e3ecafd8159a5d73bfee5ecd1a151bdf8fe02');

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'scheduled_job_runs') THEN
CREATE TABLE `scheduled_job_runs` (
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `run_identity` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `job_id` bigint unsigned NOT NULL,
  `kind` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `scheduled_for` datetime(3) DEFAULT NULL,
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  `duration_ms` int unsigned NOT NULL DEFAULT 0,
  `message_id` varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  `error_message` varchar(500) DEFAULT NULL,
  `triggered_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `triggered_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `triggered_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `triggered_by_display_name` varchar(100) DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  PRIMARY KEY (`run_id`),
  UNIQUE KEY `uq_scheduled_job_runs_identity` (`run_identity`),
  KEY `idx_scheduled_job_runs_job_cursor` (`job_id`, `started_at`, `run_id`),
  KEY `idx_scheduled_job_runs_result_cursor` (`result`, `started_at`, `run_id`),
  KEY `idx_scheduled_job_runs_kind_cursor` (`kind`, `started_at`, `run_id`),
  KEY `idx_scheduled_job_runs_actor_cursor` (`triggered_by_user_id`, `started_at`, `run_id`),
  CONSTRAINT `fk_scheduled_job_runs_job` FOREIGN KEY (`job_id`) REFERENCES `scheduled_jobs` (`id`),
  CONSTRAINT `chk_scheduled_job_runs_kind` CHECK (`kind` IN ('scheduled', 'test')),
  CONSTRAINT `chk_scheduled_job_runs_result` CHECK (`result` IN ('success', 'failed', 'unknown', 'skipped')),
  CONSTRAINT `chk_scheduled_job_runs_actor_type` CHECK (`triggered_by_type` IS NULL OR `triggered_by_type` IN ('admin_user', 'qq_user', 'system'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('scheduled_job_runs', '7b6e394550104fca674c7235672152db5b6b1863290476d0f916fb3ed4499861');
-- jxh:008-stage table-scheduled_job_runs

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'bot_operation_events') THEN
CREATE TABLE `bot_operation_events` (
  `event_id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_type` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint DEFAULT NULL,
  `feature_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `actor_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `job_id` bigint unsigned DEFAULT NULL,
  `outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `duration_ms` int unsigned DEFAULT NULL,
  `metadata` json DEFAULT NULL,
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`event_id`),
  KEY `idx_bot_events_time_cursor` (`occurred_at`, `event_id`),
  KEY `idx_bot_events_type_cursor` (`event_type`, `occurred_at`, `event_id`),
  KEY `idx_bot_events_group_type_cursor` (`group_id`, `event_type`, `occurred_at`, `event_id`),
  KEY `idx_bot_events_feature_outcome_cursor` (`feature_key`, `outcome`, `occurred_at`, `event_id`),
  KEY `idx_bot_events_command_cursor` (`command_id`, `occurred_at`, `event_id`),
  KEY `idx_bot_events_job_cursor` (`job_id`, `occurred_at`, `event_id`),
  CONSTRAINT `chk_bot_events_feature_key` CHECK (`feature_key` IS NULL OR `feature_key` = '' OR `feature_key` IN ('keyword_reply', 'ai_qa', 'quote', 'link_cleaner', 'welcome', 'custom_commands')),
  CONSTRAINT `chk_bot_events_outcome` CHECK (`outcome` IS NULL OR `outcome` = '' OR `outcome` IN ('success', 'failed', 'denied', 'unknown', 'fallback', 'skipped'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('bot_operation_events', 'a0f59b07be160135258243c2174f0c3ebb99605cf7cb489fe474c4d9be30def2');
-- jxh:008-stage table-bot_operation_events

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'bot_operation_daily') THEN
CREATE TABLE `bot_operation_daily` (
  `bucket_date` date NOT NULL,
  `timezone` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `metric_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint NOT NULL DEFAULT 0 COMMENT '0 means all groups',
  `feature_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' COMMENT 'empty means all features',
  `outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' COMMENT 'empty means all outcomes',
  `value_count` bigint unsigned NOT NULL DEFAULT 0,
  `value_sum` decimal(24,6) NOT NULL DEFAULT 0,
  `sample_count` bigint unsigned NOT NULL DEFAULT 0,
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`bucket_date`, `timezone`, `metric_key`, `group_id`, `feature_key`, `outcome`),
  KEY `idx_bot_daily_metric_date` (`metric_key`, `bucket_date`, `group_id`, `feature_key`, `outcome`),
  KEY `idx_bot_daily_group_date` (`group_id`, `bucket_date`, `metric_key`, `feature_key`, `outcome`),
  KEY `idx_bot_daily_feature_outcome_date` (`feature_key`, `outcome`, `bucket_date`, `metric_key`, `group_id`),
  CONSTRAINT `chk_bot_daily_group_sentinel` CHECK (`group_id` >= 0),
  CONSTRAINT `chk_bot_daily_feature_key` CHECK (`feature_key` = '' OR `feature_key` IN ('keyword_reply', 'ai_qa', 'quote', 'link_cleaner', 'welcome', 'custom_commands')),
  CONSTRAINT `chk_bot_daily_outcome` CHECK (`outcome` = '' OR `outcome` IN ('success', 'failed', 'denied', 'unknown', 'fallback', 'skipped')),
  CONSTRAINT `chk_bot_daily_metric_key` CHECK (`metric_key` IN ('keyword_reply_count', 'ai_request_count', 'ai_success_rate', 'ai_duration_ms', 'join_request_count', 'manual_approval_count', 'automatic_approval_count', 'scheduled_job_run_count', 'group_message_count', 'command_run_count', 'active_user_count', 'link_clean_count', 'quote_success_count', 'quote_fallback_count', 'quote_failure_count'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('bot_operation_daily', 'eaf3b887e72a8f7594be2cd4e3ccff39aca648a58e63301e91a398109162b800');
-- jxh:008-stage table-bot_operation_daily

IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE `table_schema` = DATABASE() AND BINARY `table_name` = BINARY 'system_operations') THEN
CREATE TABLE `system_operations` (
  `operation_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `type` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `requested_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `requested_by` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `idempotency_id` bigint unsigned DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `requested_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  PRIMARY KEY (`operation_id`),
  UNIQUE KEY `uq_system_operations_request` (`request_id`),
  KEY `idx_system_operations_status_cursor` (`status`, `requested_at`, `operation_id`),
  KEY `idx_system_operations_type_cursor` (`type`, `requested_at`, `operation_id`),
  KEY `idx_system_operations_idempotency` (`idempotency_id`),
  KEY `idx_system_operations_cleanup` (`completed_at`, `operation_id`),
  CONSTRAINT `fk_system_operations_idempotency` FOREIGN KEY (`idempotency_id`) REFERENCES `admin_idempotency_keys` (`idempotency_id`) ON DELETE SET NULL,
  CONSTRAINT `chk_system_operations_type` CHECK (`type` = 'napcat_restart'),
  CONSTRAINT `chk_system_operations_status` CHECK (`status` IN ('accepted', 'running', 'succeeded', 'failed', 'unknown')),
  CONSTRAINT `chk_system_operations_actor_type` CHECK (`requested_by_type` IN ('admin_user', 'qq_user', 'system')),
  CONSTRAINT `chk_system_operations_completion` CHECK ((`status` IN ('accepted', 'running') AND `completed_at` IS NULL) OR (`status` IN ('succeeded', 'failed', 'unknown') AND `completed_at` IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
END IF;
CALL `jxh_assert_table_008`('system_operations', 'd29c683702f3c7fc0e8df8fe7682171fcbe2a054755f4447812beb9abdd853b6');
-- jxh:008-stage table-system_operations
END$$
DELIMITER ;
CALL `jxh_create_manager_tables_008`();
DROP PROCEDURE `jxh_create_manager_tables_008`;

DROP PROCEDURE IF EXISTS `jxh_guard_session_triggers_008`;
DELIMITER $$
CREATE PROCEDURE `jxh_guard_session_triggers_008`()
BEGIN
  DECLARE `trigger_count` int DEFAULT 0;
  DECLARE `exact_trigger_count` int DEFAULT 0;

  SELECT COUNT(*), COALESCE(SUM(
    (BINARY `trigger_name` = BINARY 'trg_admin_sessions_replacement_insert'
      AND BINARY SHA2(CONCAT_WS(':', `action_timing`, `event_manipulation`, `action_orientation`, `action_statement`), 256)
        = BINARY 'e8773c9b9aec72e6c8158bc95d37c703007ca707df707db93469d3f399489df0')
    OR
    (BINARY `trigger_name` = BINARY 'trg_admin_sessions_replacement_update'
      AND BINARY SHA2(CONCAT_WS(':', `action_timing`, `event_manipulation`, `action_orientation`, `action_statement`), 256)
        = BINARY 'ed94c36fa0fc2a9155e16a92a2400df7332e0cd295c0f5d248c9bd8af68958b0')
  ), 0)
  INTO `trigger_count`, `exact_trigger_count`
  FROM information_schema.triggers
  WHERE `trigger_schema` = DATABASE()
    AND BINARY `event_object_table` = BINARY 'admin_sessions';

  IF `trigger_count` <> `exact_trigger_count` THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'admin session trigger structure mismatch';
  END IF;
END$$
DELIMITER ;
CALL `jxh_guard_session_triggers_008`();

DROP TRIGGER IF EXISTS `trg_admin_sessions_replacement_insert`;
DROP TRIGGER IF EXISTS `trg_admin_sessions_replacement_update`;
-- jxh:008-stage session-trigger-drop

DELIMITER $$
CREATE TRIGGER `trg_admin_sessions_replacement_insert`
BEFORE INSERT ON `admin_sessions`
FOR EACH ROW
BEGIN
  IF NOT (
    (NEW.replaced_by_session_id IS NULL AND NEW.replaced_by_user_id IS NULL AND NEW.replaced_by_depth IS NULL)
    OR (NEW.replaced_by_session_id IS NOT NULL AND NEW.replaced_by_user_id IS NOT NULL AND NEW.replaced_by_depth IS NOT NULL
        AND NEW.replaced_by_user_id = NEW.user_id AND NEW.replaced_by_depth > NEW.replacement_depth
        AND NEW.status = 'revoked' AND NEW.revoked_at IS NOT NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'invalid admin session replacement';
  END IF;
END$$
DELIMITER ;
-- jxh:008-stage session-trigger-insert

DELIMITER $$
CREATE TRIGGER `trg_admin_sessions_replacement_update`
BEFORE UPDATE ON `admin_sessions`
FOR EACH ROW
BEGIN
  IF NEW.replacement_depth <> OLD.replacement_depth OR NOT (
    (NEW.replaced_by_session_id IS NULL AND NEW.replaced_by_user_id IS NULL AND NEW.replaced_by_depth IS NULL)
    OR (NEW.replaced_by_session_id IS NOT NULL AND NEW.replaced_by_user_id IS NOT NULL AND NEW.replaced_by_depth IS NOT NULL
        AND NEW.replaced_by_user_id = NEW.user_id AND NEW.replaced_by_depth > NEW.replacement_depth
        AND NEW.status = 'revoked' AND NEW.revoked_at IS NOT NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'invalid admin session replacement';
  END IF;
END$$
DELIMITER ;
-- jxh:008-stage session-trigger-update

CALL `jxh_guard_session_triggers_008`();
DROP PROCEDURE `jxh_guard_session_triggers_008`;
DROP PROCEDURE `jxh_assert_table_008`;

-- 009_support_knowledge_reload_operations
-- Knowledge reloads share the durable operation and idempotency lifecycle used
-- by other externally visible management actions.
DROP PROCEDURE IF EXISTS `jxh_extend_system_operations_009`;
DELIMITER $$
CREATE PROCEDURE `jxh_extend_system_operations_009`()
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.table_constraints
    WHERE constraint_schema = DATABASE()
      AND BINARY table_name = BINARY 'system_operations'
      AND BINARY constraint_name = BINARY 'chk_system_operations_type'
      AND BINARY constraint_type = BINARY 'CHECK'
  ) THEN
    ALTER TABLE `system_operations` DROP CHECK `chk_system_operations_type`;
  END IF;

  ALTER TABLE `system_operations`
    ADD CONSTRAINT `chk_system_operations_type`
    CHECK (`type` IN ('napcat_restart', 'knowledge_reload'));
END$$
DELIMITER ;
CALL `jxh_extend_system_operations_009`();
DROP PROCEDURE `jxh_extend_system_operations_009`;

-- 010_add_student_id_rules
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

-- 011_enable_automatic_join_rejection
-- Permit the existing per-group policy flag to reject AI-validated invalid applications.
-- Existing rows remain false; enabling rejection always requires an explicit policy update.
ALTER TABLE `group_join_policies`
  DROP CHECK `chk_group_join_policies_auto_reject`;

CREATE TABLE IF NOT EXISTS `schema_migrations` (
  `version` int unsigned NOT NULL,
  `name` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `checksum` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `applied_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`version`),
  UNIQUE KEY `uq_schema_migrations_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `schema_migration_attempts` (
  `version` int unsigned NOT NULL,
  `name` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `checksum` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `stage` int unsigned NOT NULL DEFAULT 0,
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`version`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES
  (1, '001_create_core_schema', '81f71d4c8db2a412f0f9b0f1d4d61d6d53ecc538b801b2c365d570f789fa66a9', CURRENT_TIMESTAMP(3)),
  (2, '002_add_run_date_to_scheduled_jobs', 'df0b7f62b0e0465a64d7c90b9d798cada31159ffa683e22abceab41724d395fa', CURRENT_TIMESTAMP(3)),
  (3, '003_expand_group_request_flag', '0703d0fe865e6865d0047ae84ba75ab9a9506b72d90c60f74c3b36051ac11306', CURRENT_TIMESTAMP(3)),
  (4, '004_use_binary_collation_for_identifiers', '254b502311291b48f7002c041fb6c96cad16f4386aa26e195a6bf373aa41bf17', CURRENT_TIMESTAMP(3)),
  (5, '005_automate_group_request_processing', 'a2239296a829056b33833806a7a064ab6db7ad677f915c723bfe21cd92f9bdae', CURRENT_TIMESTAMP(3)),
  (6, '006_reparse_group_request_applicants', '42ad208b9fcbf9990fc295979d17b037bc7050410e9440b2dcffa46fae8e6248', CURRENT_TIMESTAMP(3)),
  (7, '007_remove_group_request_system_request_id', '94c4e2d5edb46c0c920540684c63585973efa419c321cdcadc9e69e779ada971', CURRENT_TIMESTAMP(3)),
  (8, '008_create_manager_schema', 'a52e9d085d265ebb39339e57931d95bbc396f2a4c3b675559b9dec0430a25db9', CURRENT_TIMESTAMP(3)),
  (9, '009_support_knowledge_reload_operations', 'b0ddb67f10af91b6ff7b9b4e94276c5bc8f1f5a3e4205de78cfd48e8712e620e', CURRENT_TIMESTAMP(3)),
  (10, '010_add_student_id_rules', '88f21f5ff3e088e8b9be196c7cb3cc129451235e5bb580a0cfa713da4364571b', CURRENT_TIMESTAMP(3)),
  (11, '011_enable_automatic_join_rejection', '3fcb3f67001e95141787c891c4e8751de1bd8501b9074d3dcfe9e66d687560c4', CURRENT_TIMESTAMP(3));
