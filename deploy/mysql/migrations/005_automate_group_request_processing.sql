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
