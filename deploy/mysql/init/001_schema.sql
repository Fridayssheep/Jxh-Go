-- Jxh Manager 完整 MySQL schema
-- 此文件用于 MySQL 8.4 容器的首次初始化，等效于按顺序执行 001-008 迁移。

SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `schema_migrations` (
  `version` int unsigned NOT NULL,
  `name` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `checksum` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `applied_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`version`),
  UNIQUE KEY `uq_schema_migrations_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO `schema_migrations` (`version`, `name`, `checksum`) VALUES
(1, '001_create_core_schema', '81f71d4c8db2a412f0f9b0f1d4d61d6d53ecc538b801b2c365d570f789fa66a9'),
(2, '002_add_run_date_to_scheduled_jobs', 'df0b7f62b0e0465a64d7c90b9d798cada31159ffa683e22abceab41724d395fa'),
(3, '003_expand_group_request_flag', '0703d0fe865e6865d0047ae84ba75ab9a9506b72d90c60f74c3b36051ac11306'),
(4, '004_use_binary_collation_for_identifiers', '254b502311291b48f7002c041fb6c96cad16f4386aa26e195a6bf373aa41bf17'),
(5, '005_automate_group_request_processing', 'a2239296a829056b33833806a7a064ab6db7ad677f915c723bfe21cd92f9bdae'),
(6, '006_reparse_group_request_applicants', '42ad208b9fcbf9990fc295979d17b037bc7050410e9440b2dcffa46fae8e6248'),
(7, '007_remove_group_request_system_request_id', 'e451e5b2f0896dc444e73b22955b5a5006e41a846b9e7d7217e9f8a0cda23207'),
(8, '008_create_manager_schema', '29edb61acd15775b3fa383a8f83315b00e219bcca7e4610859b174f7f7275d43');
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
ALTER TABLE `scheduled_jobs`
ADD COLUMN `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL'
AFTER `time_hhmm`;

UPDATE `scheduled_jobs`
SET `run_date` = CURRENT_DATE
WHERE `type` = '单次' AND `run_date` IS NULL AND `enabled` = TRUE;
-- 没有 flag 的旧记录无法对应 NapCat 群通知，不能参与后续去重或处理。
DELETE FROM `group_join_requests`
WHERE `flag` IS NULL OR `flag` = '';

ALTER TABLE `group_join_requests`
  DROP INDEX `idx_group_join_requests_request_key`,
  MODIFY COLUMN `flag` varchar(512) NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串',
  ADD UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  DROP COLUMN `request_key`;
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
-- 移除 group_join_requests.system_request_id 冗余列
-- 该列与 flag 平行存储相同数据，现已统一使用 flag

-- 验证数据一致性：确保 system_request_id 为 NULL 或与 flag 一致
SET @mismatch_count = (
  SELECT COUNT(*)
  FROM `group_join_requests`
  WHERE `system_request_id` IS NOT NULL
    AND BINARY `system_request_id` != BINARY `flag`
);

SELECT IF(@mismatch_count > 0,
  (SELECT SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'system_request_id and flag mismatch detected'),
  1
);

-- 删除冗余索引和列
ALTER TABLE `group_join_requests`
  DROP INDEX IF EXISTS `idx_group_join_requests_system_request_id`,
  DROP COLUMN IF EXISTS `system_request_id`;
-- 创建 Jxh Manager 管理平台所需的全部表

-- 管理员账号
CREATE TABLE `admin_users` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '登录用户名',
  `password_hash` varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'Argon2id 密码哈希',
  `display_name` varchar(100) NOT NULL COMMENT '显示名称',
  `role` varchar(32) NOT NULL COMMENT '角色：super_admin/maintainer/observer',
  `qq_user_id` bigint DEFAULT NULL COMMENT '可选的 QQ 绑定，用于 QQ 命令身份识别',
  `enabled` boolean NOT NULL DEFAULT TRUE COMMENT '是否启用',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_users_username` (`username`),
  UNIQUE KEY `uq_admin_users_qq_user_id` (`qq_user_id`),
  KEY `idx_admin_users_role` (`role`),
  KEY `idx_admin_users_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理员账号';

-- 管理端会话
CREATE TABLE `admin_sessions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '会话标识符',
  `token_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '令牌哈希',
  `user_id` bigint unsigned NOT NULL COMMENT '所属用户',
  `ip_address` varchar(45) DEFAULT NULL COMMENT '客户端 IP',
  `user_agent` varchar(500) DEFAULT NULL COMMENT '客户端 UA',
  `status` varchar(32) NOT NULL DEFAULT 'active' COMMENT '状态：active/revoked',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_used_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `expires_at` datetime(3) NOT NULL COMMENT '绝对过期时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_sessions_session_id` (`session_id`),
  UNIQUE KEY `uq_admin_sessions_token_hash` (`token_hash`),
  KEY `idx_admin_sessions_user_id` (`user_id`),
  KEY `idx_admin_sessions_status` (`status`),
  KEY `idx_admin_sessions_expires_at` (`expires_at`),
  CONSTRAINT `fk_admin_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `admin_users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理端会话';

-- 审计日志
CREATE TABLE `admin_audit_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `audit_log_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '审计日志 ID',
  `actor_type` varchar(32) NOT NULL COMMENT '操作者类型：admin_user/qq_user/system',
  `actor_user_id` bigint unsigned DEFAULT NULL COMMENT '管理员用户 ID',
  `actor_qq_user_id` bigint DEFAULT NULL COMMENT 'QQ 用户 ID',
  `action` varchar(100) NOT NULL COMMENT '操作动作',
  `target_type` varchar(64) DEFAULT NULL COMMENT '目标类型',
  `target_id` varchar(256) DEFAULT NULL COMMENT '目标 ID',
  `scope` varchar(64) DEFAULT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '请求 ID',
  `before_value` mediumtext DEFAULT NULL COMMENT '变更前值（脱敏）',
  `after_value` mediumtext DEFAULT NULL COMMENT '变更后值（脱敏）',
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_audit_logs_audit_log_id` (`audit_log_id`),
  KEY `idx_admin_audit_logs_actor` (`actor_type`, `actor_user_id`),
  KEY `idx_admin_audit_logs_action` (`action`),
  KEY `idx_admin_audit_logs_target` (`target_type`, `target_id`(64)),
  KEY `idx_admin_audit_logs_occurred_at` (`occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理操作审计日志';

-- 群目录快照
CREATE TABLE `managed_groups` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `group_id` bigint NOT NULL COMMENT 'QQ 群号',
  `group_name` varchar(255) DEFAULT NULL COMMENT '群名称',
  `member_count` int DEFAULT NULL COMMENT '成员数',
  `bot_role` varchar(32) DEFAULT NULL COMMENT 'bot 角色：member/admin/owner',
  `synced_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '最后同步时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_managed_groups_group_id` (`group_id`),
  KEY `idx_managed_groups_synced_at` (`synced_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='NapCat 群目录快照';

-- 功能设置
CREATE TABLE `feature_settings` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `scope` varchar(32) NOT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `feature_key` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '功能键',
  `enabled` boolean DEFAULT NULL COMMENT '启用状态：true/false/null(继承)',
  `config_json` mediumtext DEFAULT NULL COMMENT '功能配置 JSON',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_feature_settings_scope` (`scope`, `scope_group_id`, `feature_key`),
  KEY `idx_feature_settings_feature_key` (`feature_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='功能设置';

-- 自定义命令
CREATE TABLE `custom_commands` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '命令 ID',
  `name` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '命令名（不含 /）',
  `display_name` varchar(100) NOT NULL COMMENT '显示名称',
  `description` varchar(500) DEFAULT NULL COMMENT '描述',
  `scope` varchar(32) NOT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `permission` varchar(32) NOT NULL COMMENT '权限：all_members/admins_only',
  `actions_json` mediumtext NOT NULL COMMENT '动作定义 JSON',
  `enabled` boolean NOT NULL DEFAULT TRUE COMMENT '是否启用',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_custom_commands_command_id` (`command_id`),
  UNIQUE KEY `uq_custom_commands_scope_name` (`scope`, `scope_group_id`, `name`),
  KEY `idx_custom_commands_scope` (`scope`, `scope_group_id`),
  KEY `idx_custom_commands_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='自定义命令';

-- 自定义命令执行记录
CREATE TABLE `custom_command_runs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '执行 ID',
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '命令 ID',
  `group_id` bigint NOT NULL COMMENT '执行所在群',
  `user_id` bigint NOT NULL COMMENT '触发用户',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/partial',
  `outcomes_json` mediumtext DEFAULT NULL COMMENT '逐步执行结果 JSON',
  `executed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_custom_command_runs_run_id` (`run_id`),
  KEY `idx_custom_command_runs_command_id` (`command_id`),
  KEY `idx_custom_command_runs_group_id` (`group_id`),
  KEY `idx_custom_command_runs_executed_at` (`executed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='自定义命令执行记录';

-- 入群申请决策记录
CREATE TABLE `group_join_decisions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '决策 ID',
  `request_id` bigint unsigned NOT NULL COMMENT '申请 ID',
  `idempotency_key` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '幂等键',
  `actor_type` varchar(32) NOT NULL COMMENT '操作者类型：admin_user/system',
  `actor_user_id` bigint unsigned DEFAULT NULL COMMENT '管理员用户 ID',
  `decision` varchar(32) NOT NULL COMMENT '决策：approve/reject',
  `reason` varchar(500) DEFAULT NULL COMMENT '决策原因',
  `snapshot_json` mediumtext DEFAULT NULL COMMENT '申请字段快照',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/unknown',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `request_id_ref` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '请求 ID',
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL COMMENT '完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_group_join_decisions_decision_id` (`decision_id`),
  UNIQUE KEY `uq_group_join_decisions_idempotency` (`actor_type`, `actor_user_id`, `idempotency_key`),
  KEY `idx_group_join_decisions_request_id` (`request_id`),
  KEY `idx_group_join_decisions_started_at` (`started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='入群申请决策记录';

-- 定时任务执行记录
CREATE TABLE `scheduled_job_runs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '执行 ID',
  `job_id` bigint unsigned NOT NULL COMMENT '任务 ID',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/unknown',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `duration_ms` int unsigned DEFAULT NULL COMMENT '执行耗时（毫秒）',
  `is_test_run` boolean NOT NULL DEFAULT FALSE COMMENT '是否测试发送',
  `executed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_scheduled_job_runs_run_id` (`run_id`),
  KEY `idx_scheduled_job_runs_job_id` (`job_id`),
  KEY `idx_scheduled_job_runs_executed_at` (`executed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='定时任务执行记录';

-- bot 操作事件明细
CREATE TABLE `bot_operation_events` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_type` varchar(64) NOT NULL COMMENT '事件类型',
  `group_id` bigint NOT NULL COMMENT '所在群',
  `user_id_hmac` char(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '用户 HMAC',
  `feature_key` varchar(64) DEFAULT NULL COMMENT '功能键',
  `result` varchar(32) NOT NULL COMMENT '结果',
  `duration_ms` int unsigned DEFAULT NULL COMMENT '耗时（毫秒）',
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_bot_operation_events_type` (`event_type`),
  KEY `idx_bot_operation_events_group_id` (`group_id`),
  KEY `idx_bot_operation_events_occurred_at` (`occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='bot 操作事件明细';

-- bot 操作日聚合
CREATE TABLE `bot_operation_daily` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `date` date NOT NULL COMMENT '统计日期',
  `event_type` varchar(64) NOT NULL COMMENT '事件类型',
  `group_id` bigint NOT NULL COMMENT '所在群',
  `feature_key` varchar(64) DEFAULT NULL COMMENT '功能键',
  `result` varchar(32) NOT NULL COMMENT '结果',
  `count` int unsigned NOT NULL DEFAULT 0 COMMENT '次数',
  `unique_users` int unsigned NOT NULL DEFAULT 0 COMMENT '去重用户数',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_bot_operation_daily` (`date`, `event_type`, `group_id`, `feature_key`, `result`),
  KEY `idx_bot_operation_daily_date` (`date`),
  KEY `idx_bot_operation_daily_group_id` (`group_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='bot 操作日聚合';

-- 扩展现有表：scheduled_jobs 增加版本字段
ALTER TABLE `scheduled_jobs`
  ADD COLUMN `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本' AFTER `enabled`;
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
ALTER TABLE `scheduled_jobs`
ADD COLUMN `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL'
AFTER `time_hhmm`;

UPDATE `scheduled_jobs`
SET `run_date` = CURRENT_DATE
WHERE `type` = '单次' AND `run_date` IS NULL AND `enabled` = TRUE;
-- 没有 flag 的旧记录无法对应 NapCat 群通知，不能参与后续去重或处理。
DELETE FROM `group_join_requests`
WHERE `flag` IS NULL OR `flag` = '';

ALTER TABLE `group_join_requests`
  DROP INDEX `idx_group_join_requests_request_key`,
  MODIFY COLUMN `flag` varchar(512) NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串',
  ADD UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  DROP COLUMN `request_key`;
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
-- 移除 group_join_requests.system_request_id 冗余列
-- 该列与 flag 平行存储相同数据，现已统一使用 flag

-- 验证数据一致性：确保 system_request_id 为 NULL 或与 flag 一致
SET @mismatch_count = (
  SELECT COUNT(*)
  FROM `group_join_requests`
  WHERE `system_request_id` IS NOT NULL
    AND BINARY `system_request_id` != BINARY `flag`
);

SELECT IF(@mismatch_count > 0,
  (SELECT SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'system_request_id and flag mismatch detected'),
  1
);

-- 删除冗余索引和列
ALTER TABLE `group_join_requests`
  DROP INDEX IF EXISTS `idx_group_join_requests_system_request_id`,
  DROP COLUMN IF EXISTS `system_request_id`;
-- 创建 Jxh Manager 管理平台所需的全部表

-- 管理员账号
CREATE TABLE `admin_users` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '登录用户名',
  `password_hash` varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'Argon2id 密码哈希',
  `display_name` varchar(100) NOT NULL COMMENT '显示名称',
  `role` varchar(32) NOT NULL COMMENT '角色：super_admin/maintainer/observer',
  `qq_user_id` bigint DEFAULT NULL COMMENT '可选的 QQ 绑定，用于 QQ 命令身份识别',
  `enabled` boolean NOT NULL DEFAULT TRUE COMMENT '是否启用',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_users_username` (`username`),
  UNIQUE KEY `uq_admin_users_qq_user_id` (`qq_user_id`),
  KEY `idx_admin_users_role` (`role`),
  KEY `idx_admin_users_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理员账号';

-- 管理端会话
CREATE TABLE `admin_sessions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '会话标识符',
  `token_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '令牌哈希',
  `user_id` bigint unsigned NOT NULL COMMENT '所属用户',
  `ip_address` varchar(45) DEFAULT NULL COMMENT '客户端 IP',
  `user_agent` varchar(500) DEFAULT NULL COMMENT '客户端 UA',
  `status` varchar(32) NOT NULL DEFAULT 'active' COMMENT '状态：active/revoked',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_used_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `expires_at` datetime(3) NOT NULL COMMENT '绝对过期时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_sessions_session_id` (`session_id`),
  UNIQUE KEY `uq_admin_sessions_token_hash` (`token_hash`),
  KEY `idx_admin_sessions_user_id` (`user_id`),
  KEY `idx_admin_sessions_status` (`status`),
  KEY `idx_admin_sessions_expires_at` (`expires_at`),
  CONSTRAINT `fk_admin_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `admin_users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理端会话';

-- 审计日志
CREATE TABLE `admin_audit_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `audit_log_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '审计日志 ID',
  `actor_type` varchar(32) NOT NULL COMMENT '操作者类型：admin_user/qq_user/system',
  `actor_user_id` bigint unsigned DEFAULT NULL COMMENT '管理员用户 ID',
  `actor_qq_user_id` bigint DEFAULT NULL COMMENT 'QQ 用户 ID',
  `action` varchar(100) NOT NULL COMMENT '操作动作',
  `target_type` varchar(64) DEFAULT NULL COMMENT '目标类型',
  `target_id` varchar(256) DEFAULT NULL COMMENT '目标 ID',
  `scope` varchar(64) DEFAULT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '请求 ID',
  `before_value` mediumtext DEFAULT NULL COMMENT '变更前值（脱敏）',
  `after_value` mediumtext DEFAULT NULL COMMENT '变更后值（脱敏）',
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_admin_audit_logs_audit_log_id` (`audit_log_id`),
  KEY `idx_admin_audit_logs_actor` (`actor_type`, `actor_user_id`),
  KEY `idx_admin_audit_logs_action` (`action`),
  KEY `idx_admin_audit_logs_target` (`target_type`, `target_id`(64)),
  KEY `idx_admin_audit_logs_occurred_at` (`occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='管理操作审计日志';

-- 群目录快照
CREATE TABLE `managed_groups` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `group_id` bigint NOT NULL COMMENT 'QQ 群号',
  `group_name` varchar(255) DEFAULT NULL COMMENT '群名称',
  `member_count` int DEFAULT NULL COMMENT '成员数',
  `bot_role` varchar(32) DEFAULT NULL COMMENT 'bot 角色：member/admin/owner',
  `synced_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '最后同步时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_managed_groups_group_id` (`group_id`),
  KEY `idx_managed_groups_synced_at` (`synced_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='NapCat 群目录快照';

-- 功能设置
CREATE TABLE `feature_settings` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `scope` varchar(32) NOT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `feature_key` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '功能键',
  `enabled` boolean DEFAULT NULL COMMENT '启用状态：true/false/null(继承)',
  `config_json` mediumtext DEFAULT NULL COMMENT '功能配置 JSON',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_feature_settings_scope` (`scope`, `scope_group_id`, `feature_key`),
  KEY `idx_feature_settings_feature_key` (`feature_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='功能设置';

-- 自定义命令
CREATE TABLE `custom_commands` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '命令 ID',
  `name` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '命令名（不含 /）',
  `display_name` varchar(100) NOT NULL COMMENT '显示名称',
  `description` varchar(500) DEFAULT NULL COMMENT '描述',
  `scope` varchar(32) NOT NULL COMMENT '作用域：global/group',
  `scope_group_id` bigint DEFAULT NULL COMMENT '群作用域 ID',
  `permission` varchar(32) NOT NULL COMMENT '权限：all_members/admins_only',
  `actions_json` mediumtext NOT NULL COMMENT '动作定义 JSON',
  `enabled` boolean NOT NULL DEFAULT TRUE COMMENT '是否启用',
  `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_custom_commands_command_id` (`command_id`),
  UNIQUE KEY `uq_custom_commands_scope_name` (`scope`, `scope_group_id`, `name`),
  KEY `idx_custom_commands_scope` (`scope`, `scope_group_id`),
  KEY `idx_custom_commands_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='自定义命令';

-- 自定义命令执行记录
CREATE TABLE `custom_command_runs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '执行 ID',
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '命令 ID',
  `group_id` bigint NOT NULL COMMENT '执行所在群',
  `user_id` bigint NOT NULL COMMENT '触发用户',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/partial',
  `outcomes_json` mediumtext DEFAULT NULL COMMENT '逐步执行结果 JSON',
  `executed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_custom_command_runs_run_id` (`run_id`),
  KEY `idx_custom_command_runs_command_id` (`command_id`),
  KEY `idx_custom_command_runs_group_id` (`group_id`),
  KEY `idx_custom_command_runs_executed_at` (`executed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='自定义命令执行记录';

-- 入群申请决策记录
CREATE TABLE `group_join_decisions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '决策 ID',
  `request_id` bigint unsigned NOT NULL COMMENT '申请 ID',
  `idempotency_key` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '幂等键',
  `actor_type` varchar(32) NOT NULL COMMENT '操作者类型：admin_user/system',
  `actor_user_id` bigint unsigned DEFAULT NULL COMMENT '管理员用户 ID',
  `decision` varchar(32) NOT NULL COMMENT '决策：approve/reject',
  `reason` varchar(500) DEFAULT NULL COMMENT '决策原因',
  `snapshot_json` mediumtext DEFAULT NULL COMMENT '申请字段快照',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/unknown',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `request_id_ref` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '请求 ID',
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL COMMENT '完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_group_join_decisions_decision_id` (`decision_id`),
  UNIQUE KEY `uq_group_join_decisions_idempotency` (`actor_type`, `actor_user_id`, `idempotency_key`),
  KEY `idx_group_join_decisions_request_id` (`request_id`),
  KEY `idx_group_join_decisions_started_at` (`started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='入群申请决策记录';

-- 定时任务执行记录
CREATE TABLE `scheduled_job_runs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '执行 ID',
  `job_id` bigint unsigned NOT NULL COMMENT '任务 ID',
  `result` varchar(32) NOT NULL COMMENT '结果：success/failed/unknown',
  `error_code` varchar(100) DEFAULT NULL COMMENT '错误代码',
  `duration_ms` int unsigned DEFAULT NULL COMMENT '执行耗时（毫秒）',
  `is_test_run` boolean NOT NULL DEFAULT FALSE COMMENT '是否测试发送',
  `executed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_scheduled_job_runs_run_id` (`run_id`),
  KEY `idx_scheduled_job_runs_job_id` (`job_id`),
  KEY `idx_scheduled_job_runs_executed_at` (`executed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='定时任务执行记录';

-- bot 操作事件明细
CREATE TABLE `bot_operation_events` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_type` varchar(64) NOT NULL COMMENT '事件类型',
  `group_id` bigint NOT NULL COMMENT '所在群',
  `user_id_hmac` char(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '用户 HMAC',
  `feature_key` varchar(64) DEFAULT NULL COMMENT '功能键',
  `result` varchar(32) NOT NULL COMMENT '结果',
  `duration_ms` int unsigned DEFAULT NULL COMMENT '耗时（毫秒）',
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_bot_operation_events_type` (`event_type`),
  KEY `idx_bot_operation_events_group_id` (`group_id`),
  KEY `idx_bot_operation_events_occurred_at` (`occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='bot 操作事件明细';

-- bot 操作日聚合
CREATE TABLE `bot_operation_daily` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `date` date NOT NULL COMMENT '统计日期',
  `event_type` varchar(64) NOT NULL COMMENT '事件类型',
  `group_id` bigint NOT NULL COMMENT '所在群',
  `feature_key` varchar(64) DEFAULT NULL COMMENT '功能键',
  `result` varchar(32) NOT NULL COMMENT '结果',
  `count` int unsigned NOT NULL DEFAULT 0 COMMENT '次数',
  `unique_users` int unsigned NOT NULL DEFAULT 0 COMMENT '去重用户数',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_bot_operation_daily` (`date`, `event_type`, `group_id`, `feature_key`, `result`),
  KEY `idx_bot_operation_daily_date` (`date`),
  KEY `idx_bot_operation_daily_group_id` (`group_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
COMMENT='bot 操作日聚合';

-- 扩展现有表：scheduled_jobs 增加版本字段
ALTER TABLE `scheduled_jobs`
  ADD COLUMN `version` int unsigned NOT NULL DEFAULT 1 COMMENT '乐观并发版本' AFTER `enabled`;
