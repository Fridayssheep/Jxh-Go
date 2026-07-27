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
