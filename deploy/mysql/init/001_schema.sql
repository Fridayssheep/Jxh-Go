-- Jxh Manager final MySQL 8.4 bootstrap schema.
-- This file initializes empty databases only; running deployments are not modified.

/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;
/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;
/*!50503 SET NAMES utf8mb4 */;
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  `redacted` tinyint(1) NOT NULL DEFAULT '1',
  PRIMARY KEY (`audit_log_id`),
  KEY `idx_admin_audit_time_cursor` (`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_actor_cursor` (`actor_user_id`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_actor_role_cursor` (`actor_role`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_scope_cursor` (`scope_type`,`scope_id`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_action_cursor` (`action`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_target_cursor` (`target_type`,`target_id`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_result_cursor` (`result`,`occurred_at`,`audit_log_id`),
  KEY `idx_admin_audit_request` (`request_id`),
  CONSTRAINT `chk_admin_audit_actor_role` CHECK (((`actor_role` is null) or (`actor_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_admin_audit_actor_type` CHECK ((`actor_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_admin_audit_result` CHECK ((`result` in (_utf8mb4'success',_utf8mb4'failed',_utf8mb4'unknown'))),
  CONSTRAINT `chk_admin_audit_source` CHECK ((`source` in (_utf8mb4'web',_utf8mb4'qq',_utf8mb4'system')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  UNIQUE KEY `uq_admin_idempotency_actor_operation_key` (`actor_type`,`actor_id`,`operation`,`idempotency_key`),
  KEY `idx_admin_idempotency_expiry` (`expires_at`,`idempotency_id`),
  KEY `idx_admin_idempotency_resource` (`resource_type`,`resource_id`,`created_at`,`idempotency_id`),
  KEY `idx_admin_idempotency_session` (`resulting_session_id`),
  CONSTRAINT `fk_admin_idempotency_session` FOREIGN KEY (`resulting_session_id`) REFERENCES `admin_sessions` (`session_id`) ON DELETE SET NULL,
  CONSTRAINT `chk_admin_idempotency_actor_type` CHECK ((`actor_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_admin_idempotency_completion` CHECK ((((`state` = _utf8mb4'in_progress') and (`result_status` is null) and (`completed_at` is null)) or ((`state` = _utf8mb4'completed') and (`result_status` is not null) and (`completed_at` is not null)))),
  CONSTRAINT `chk_admin_idempotency_response_status` CHECK (((`response_status` is null) or (`response_status` between 100 and 599))),
  CONSTRAINT `chk_admin_idempotency_result_status` CHECK (((`result_status` is null) or (`result_status` in (_utf8mb4'succeeded',_utf8mb4'failed',_utf8mb4'unknown')))),
  CONSTRAINT `chk_admin_idempotency_state` CHECK ((`state` in (_utf8mb4'in_progress',_utf8mb4'completed')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  `replacement_depth` int unsigned NOT NULL DEFAULT '0',
  `replaced_by_session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `replaced_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `replaced_by_depth` int unsigned DEFAULT NULL,
  PRIMARY KEY (`session_id`),
  UNIQUE KEY `uq_admin_sessions_session_user` (`session_id`,`user_id`),
  UNIQUE KEY `uq_admin_sessions_replacement_target` (`session_id`,`user_id`,`replacement_depth`),
  UNIQUE KEY `uq_admin_sessions_token_digest` (`token_digest`),
  KEY `idx_admin_sessions_user_cursor` (`user_id`,`status`,`last_seen_at`,`session_id`),
  KEY `idx_admin_sessions_expiry` (`status`,`expires_at`,`session_id`),
  KEY `idx_admin_sessions_absolute_expiry` (`absolute_expires_at`,`session_id`),
  KEY `idx_admin_sessions_replacement` (`replaced_by_session_id`,`replaced_by_user_id`,`replaced_by_depth`),
  CONSTRAINT `fk_admin_sessions_replacement` FOREIGN KEY (`replaced_by_session_id`, `replaced_by_user_id`, `replaced_by_depth`) REFERENCES `admin_sessions` (`session_id`, `user_id`, `replacement_depth`) ON DELETE SET NULL,
  CONSTRAINT `fk_admin_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `admin_users` (`user_id`),
  CONSTRAINT `chk_admin_sessions_revocation` CHECK ((((`status` = _utf8mb4'revoked') and (`revoked_at` is not null)) or ((`status` in (_utf8mb4'active',_utf8mb4'expired')) and (`revoked_at` is null)))),
  CONSTRAINT `chk_admin_sessions_status` CHECK ((`status` in (_utf8mb4'active',_utf8mb4'expired',_utf8mb4'revoked')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!50003 SET @saved_cs_client      = @@character_set_client */ ;
/*!50003 SET @saved_cs_results     = @@character_set_results */ ;
/*!50003 SET @saved_col_connection = @@collation_connection */ ;
/*!50003 SET character_set_client  = utf8mb4 */ ;
/*!50003 SET character_set_results = utf8mb4 */ ;
/*!50003 SET collation_connection  = utf8mb4_0900_ai_ci */ ;
/*!50003 SET @saved_sql_mode       = @@sql_mode */ ;
/*!50003 SET sql_mode              = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION' */ ;
DELIMITER ;;
/*!50003 CREATE*/ /*!50003 TRIGGER `trg_admin_sessions_replacement_insert` BEFORE INSERT ON `admin_sessions` FOR EACH ROW BEGIN
  IF NOT (
    (NEW.replaced_by_session_id IS NULL AND NEW.replaced_by_user_id IS NULL AND NEW.replaced_by_depth IS NULL)
    OR (NEW.replaced_by_session_id IS NOT NULL AND NEW.replaced_by_user_id IS NOT NULL AND NEW.replaced_by_depth IS NOT NULL
        AND NEW.replaced_by_user_id = NEW.user_id AND NEW.replaced_by_depth > NEW.replacement_depth
        AND NEW.status = 'revoked' AND NEW.revoked_at IS NOT NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'invalid admin session replacement';
  END IF;
END */;;
DELIMITER ;
/*!50003 SET sql_mode              = @saved_sql_mode */ ;
/*!50003 SET character_set_client  = @saved_cs_client */ ;
/*!50003 SET character_set_results = @saved_cs_results */ ;
/*!50003 SET collation_connection  = @saved_col_connection */ ;
/*!50003 SET @saved_cs_client      = @@character_set_client */ ;
/*!50003 SET @saved_cs_results     = @@character_set_results */ ;
/*!50003 SET @saved_col_connection = @@collation_connection */ ;
/*!50003 SET character_set_client  = utf8mb4 */ ;
/*!50003 SET character_set_results = utf8mb4 */ ;
/*!50003 SET collation_connection  = utf8mb4_0900_ai_ci */ ;
/*!50003 SET @saved_sql_mode       = @@sql_mode */ ;
/*!50003 SET sql_mode              = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION' */ ;
DELIMITER ;;
/*!50003 CREATE*/ /*!50003 TRIGGER `trg_admin_sessions_replacement_update` BEFORE UPDATE ON `admin_sessions` FOR EACH ROW BEGIN
  IF NEW.replacement_depth <> OLD.replacement_depth OR NOT (
    (NEW.replaced_by_session_id IS NULL AND NEW.replaced_by_user_id IS NULL AND NEW.replaced_by_depth IS NULL)
    OR (NEW.replaced_by_session_id IS NOT NULL AND NEW.replaced_by_user_id IS NOT NULL AND NEW.replaced_by_depth IS NOT NULL
        AND NEW.replaced_by_user_id = NEW.user_id AND NEW.replaced_by_depth > NEW.replacement_depth
        AND NEW.status = 'revoked' AND NEW.revoked_at IS NOT NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'invalid admin session replacement';
  END IF;
END */;;
DELIMITER ;
/*!50003 SET sql_mode              = @saved_sql_mode */ ;
/*!50003 SET character_set_client  = @saved_cs_client */ ;
/*!50003 SET character_set_results = @saved_cs_results */ ;
/*!50003 SET collation_connection  = @saved_col_connection */ ;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `admin_users` (
  `user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `username` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `display_name` varchar(64) NOT NULL,
  `password_hash` varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `last_login_at` datetime(3) DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`user_id`),
  UNIQUE KEY `uq_admin_users_username` (`username`),
  UNIQUE KEY `uq_admin_users_qq_user_id` (`qq_user_id`),
  KEY `idx_admin_users_role_enabled_cursor` (`role`,`enabled`,`updated_at`,`user_id`),
  KEY `idx_admin_users_enabled_cursor` (`enabled`,`updated_at`,`user_id`),
  CONSTRAINT `chk_admin_users_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_admin_users_role` CHECK ((`role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `bot_operation_daily` (
  `bucket_date` date NOT NULL,
  `timezone` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `metric_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint NOT NULL DEFAULT '0' COMMENT '0 means all groups',
  `feature_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' COMMENT 'empty means all features',
  `outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' COMMENT 'empty means all outcomes',
  `value_count` bigint unsigned NOT NULL DEFAULT '0',
  `value_sum` decimal(24,6) NOT NULL DEFAULT '0.000000',
  `sample_count` bigint unsigned NOT NULL DEFAULT '0',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`bucket_date`,`timezone`,`metric_key`,`group_id`,`feature_key`,`outcome`),
  KEY `idx_bot_daily_metric_date` (`metric_key`,`bucket_date`,`group_id`,`feature_key`,`outcome`),
  KEY `idx_bot_daily_group_date` (`group_id`,`bucket_date`,`metric_key`,`feature_key`,`outcome`),
  KEY `idx_bot_daily_feature_outcome_date` (`feature_key`,`outcome`,`bucket_date`,`metric_key`,`group_id`),
  CONSTRAINT `chk_bot_daily_feature_key` CHECK (((`feature_key` = _utf8mb4'') or (`feature_key` in (_utf8mb4'keyword_reply',_utf8mb4'ai_qa',_utf8mb4'quote',_utf8mb4'link_cleaner',_utf8mb4'welcome',_utf8mb4'custom_commands')))),
  CONSTRAINT `chk_bot_daily_group_sentinel` CHECK ((`group_id` >= 0)),
  CONSTRAINT `chk_bot_daily_metric_key` CHECK ((`metric_key` in (_utf8mb4'keyword_reply_count',_utf8mb4'knowledge_trigger_count',_utf8mb4'ai_request_count',_utf8mb4'ai_success_rate',_utf8mb4'ai_duration_ms',_utf8mb4'join_request_count',_utf8mb4'manual_approval_count',_utf8mb4'automatic_approval_count',_utf8mb4'scheduled_job_run_count',_utf8mb4'group_message_count',_utf8mb4'command_run_count',_utf8mb4'active_user_count',_utf8mb4'link_clean_count',_utf8mb4'quote_success_count',_utf8mb4'quote_fallback_count',_utf8mb4'quote_failure_count'))),
  CONSTRAINT `chk_bot_daily_outcome` CHECK (((`outcome` = _utf8mb4'') or (`outcome` in (_utf8mb4'success',_utf8mb4'failed',_utf8mb4'denied',_utf8mb4'unknown',_utf8mb4'fallback',_utf8mb4'skipped'))))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  KEY `idx_bot_events_time_cursor` (`occurred_at`,`event_id`),
  KEY `idx_bot_events_type_cursor` (`event_type`,`occurred_at`,`event_id`),
  KEY `idx_bot_events_group_type_cursor` (`group_id`,`event_type`,`occurred_at`,`event_id`),
  KEY `idx_bot_events_feature_outcome_cursor` (`feature_key`,`outcome`,`occurred_at`,`event_id`),
  KEY `idx_bot_events_command_cursor` (`command_id`,`occurred_at`,`event_id`),
  KEY `idx_bot_events_job_cursor` (`job_id`,`occurred_at`,`event_id`),
  CONSTRAINT `chk_bot_events_feature_key` CHECK (((`feature_key` is null) or (`feature_key` = _utf8mb4'') or (`feature_key` in (_utf8mb4'keyword_reply',_utf8mb4'ai_qa',_utf8mb4'quote',_utf8mb4'link_cleaner',_utf8mb4'welcome',_utf8mb4'custom_commands')))),
  CONSTRAINT `chk_bot_events_outcome` CHECK (((`outcome` is null) or (`outcome` = _utf8mb4'') or (`outcome` in (_utf8mb4'success',_utf8mb4'failed',_utf8mb4'denied',_utf8mb4'unknown',_utf8mb4'fallback',_utf8mb4'skipped'))))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `custom_command_runs` (
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `run_identity` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `command_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `command_name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint NOT NULL,
  `triggered_by_qq` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `action_steps` json NOT NULL,
  `duration_ms` int unsigned NOT NULL DEFAULT '0',
  `error_code` varchar(100) DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`run_id`),
  UNIQUE KEY `uq_custom_command_runs_identity` (`run_identity`),
  KEY `idx_custom_command_runs_command_cursor` (`command_id`,`occurred_at`,`run_id`),
  KEY `idx_custom_command_runs_group_cursor` (`group_id`,`occurred_at`,`run_id`),
  KEY `idx_custom_command_runs_result_cursor` (`result`,`occurred_at`,`run_id`),
  CONSTRAINT `chk_custom_command_runs_result` CHECK ((`result` in (_utf8mb4'success',_utf8mb4'denied',_utf8mb4'parse_error',_utf8mb4'failed',_utf8mb4'partial',_utf8mb4'unknown')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  `enabled` tinyint(1) NOT NULL DEFAULT '0',
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'draft',
  `revision` int unsigned NOT NULL DEFAULT '1',
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `archived_at` datetime(3) DEFAULT NULL,
  `active_name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin GENERATED ALWAYS AS ((case when (`archived_at` is null) then `name` else NULL end)) STORED,
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uq_custom_commands_active_name` (`active_name`),
  KEY `idx_custom_commands_status_cursor` (`status`,`enabled`,`updated_at`,`command_id`),
  KEY `idx_custom_commands_scope_cursor` (`scope_type`,`updated_at`,`command_id`),
  CONSTRAINT `chk_custom_commands_actor` CHECK ((((`updated_by_type` = _utf8mb4'system') and (`updated_by_user_id` is null) and (`updated_by_qq_user_id` is null)) or ((`updated_by_type` = _utf8mb4'admin_user') and (`updated_by_user_id` is not null)) or ((`updated_by_type` = _utf8mb4'qq_user') and (`updated_by_qq_user_id` is not null)))),
  CONSTRAINT `chk_custom_commands_actor_role` CHECK (((`updated_by_role` is null) or (`updated_by_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_custom_commands_actor_type` CHECK ((`updated_by_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_custom_commands_permission` CHECK ((`trigger_permission` in (_utf8mb4'everyone',_utf8mb4'group_admin',_utf8mb4'maintenance_allowlist'))),
  CONSTRAINT `chk_custom_commands_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_custom_commands_scope` CHECK ((`scope_type` in (_utf8mb4'global',_utf8mb4'groups'))),
  CONSTRAINT `chk_custom_commands_status` CHECK ((`status` in (_utf8mb4'draft',_utf8mb4'active',_utf8mb4'disabled',_utf8mb4'archived')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `feature_settings` (
  `setting_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `scope_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `group_id` bigint DEFAULT NULL,
  `scope_key` bigint GENERATED ALWAYS AS (coalesce(`group_id`,0)) STORED,
  `settings_json` json NOT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`setting_id`),
  UNIQUE KEY `uq_feature_settings_scope` (`scope_type`,`scope_key`),
  KEY `idx_feature_settings_group_cursor` (`group_id`,`updated_at`,`setting_id`),
  KEY `idx_feature_settings_updated_cursor` (`updated_at`,`setting_id`),
  CONSTRAINT `chk_feature_settings_actor_role` CHECK (((`updated_by_role` is null) or (`updated_by_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_feature_settings_actor_type` CHECK ((`updated_by_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_feature_settings_json` CHECK ((json_type(`settings_json`) = _utf8mb4'OBJECT')),
  CONSTRAINT `chk_feature_settings_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_feature_settings_scope` CHECK ((((`scope_type` = _utf8mb4'global') and (`group_id` is null)) or ((`scope_type` = _utf8mb4'group') and (`group_id` is not null) and (`group_id` > 0))))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  `review_snapshot` json DEFAULT NULL,
  `rule_version` int unsigned DEFAULT NULL,
  `napcat_result` json DEFAULT NULL,
  `error_code` varchar(100) DEFAULT NULL,
  `trace_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`decision_id`),
  UNIQUE KEY `uq_group_join_decisions_idempotency` (`request_id`,`idempotency_key`),
  UNIQUE KEY `uq_group_join_decisions_request_ref` (`decision_id`,`request_id`),
  KEY `idx_group_join_decisions_request_cursor` (`request_id`,`started_at`,`decision_id`),
  KEY `idx_group_join_decisions_status_cursor` (`status`,`started_at`,`decision_id`),
  KEY `idx_group_join_decisions_actor_cursor` (`actor_user_id`,`started_at`,`decision_id`),
  KEY `idx_group_join_decisions_source_cursor` (`source`,`started_at`,`decision_id`),
  CONSTRAINT `fk_group_join_decisions_request` FOREIGN KEY (`request_id`) REFERENCES `group_join_requests` (`id`),
  CONSTRAINT `chk_group_join_decisions_action` CHECK ((`action` in (_utf8mb4'approve',_utf8mb4'reject'))),
  CONSTRAINT `chk_group_join_decisions_actor_role` CHECK (((`actor_role` is null) or (`actor_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_group_join_decisions_actor_type` CHECK ((`actor_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_group_join_decisions_source` CHECK ((`source` in (_utf8mb4'manual',_utf8mb4'automatic',_utf8mb4'external'))),
  CONSTRAINT `chk_group_join_decisions_status` CHECK ((`status` in (_utf8mb4'started',_utf8mb4'confirmed',_utf8mb4'failed',_utf8mb4'unknown')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `group_join_policies` (
  `group_id` bigint NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '0',
  `mode` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'ai_fields_complete',
  `required_fields` json NOT NULL,
  `auto_reject` tinyint(1) NOT NULL DEFAULT '0',
  `revision` int unsigned NOT NULL DEFAULT '1',
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`group_id`),
  KEY `idx_group_join_policies_enabled_cursor` (`enabled`,`updated_at`,`group_id`),
  CONSTRAINT `chk_group_join_policies_actor_role` CHECK (((`updated_by_role` is null) or (`updated_by_role` in (_ascii'super_admin',_ascii'maintainer',_ascii'observer')))),
  CONSTRAINT `chk_group_join_policies_actor_type` CHECK ((`updated_by_type` in (_ascii'admin_user',_ascii'qq_user',_ascii'system'))),
  CONSTRAINT `chk_group_join_policies_fields` CHECK (((json_type(`required_fields`) = _utf8mb4'ARRAY') and (json_length(`required_fields`) = 3) and (json_type(json_extract(`required_fields`,_utf8mb4'$[0]')) = _utf8mb4'STRING') and (json_type(json_extract(`required_fields`,_utf8mb4'$[1]')) = _utf8mb4'STRING') and (json_type(json_extract(`required_fields`,_utf8mb4'$[2]')) = _utf8mb4'STRING') and json_contains(`required_fields`,json_quote(_utf8mb4'student_id'),_utf8mb4'$') and json_contains(`required_fields`,json_quote(_utf8mb4'name'),_utf8mb4'$') and json_contains(`required_fields`,json_quote(_utf8mb4'major'),_utf8mb4'$'))),
  CONSTRAINT `chk_group_join_policies_mode` CHECK ((`mode` = _ascii'ai_fields_complete')),
  CONSTRAINT `chk_group_join_policies_revision` CHECK ((`revision` >= 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `group_join_requests` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `flag` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串',
  `group_id` bigint DEFAULT NULL COMMENT 'QQ群号',
  `user_id` bigint DEFAULT NULL COMMENT '申请人 QQ',
  `applicant_nickname` varchar(100) DEFAULT NULL,
  `student_id` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的学号',
  `student_name` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的姓名',
  `major` varchar(128) DEFAULT NULL COMMENT 'AI 从验证信息中提取的专业',
  `sub_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '申请类型：add/invite 等',
  `comment` text COMMENT '申请验证信息',
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '登记状态：pending/observed 等',
  `observed_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
  `decision_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
  `decision_source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `last_decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `processing_expires_at` datetime(3) DEFAULT NULL,
  `source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '来源：event/system',
  `raw_json` mediumtext COMMENT 'NapCat 原始事件或系统消息 JSON',
  `system_raw_json` mediumtext COMMENT 'NapCat 最近一次系统消息 JSON',
  `ai_parse_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending' COMMENT 'AI 解析状态：pending/running/succeeded/failed/skipped',
  `ai_parse_attempts` int unsigned NOT NULL DEFAULT '0' COMMENT 'AI 解析尝试次数',
  `ai_error_code` varchar(100) DEFAULT NULL,
  `validation_snapshot` json DEFAULT NULL,
  `automatic_review` json DEFAULT NULL,
  `requested_at` datetime(3) DEFAULT NULL COMMENT '申请时间',
  `processed_at` datetime(3) DEFAULT NULL COMMENT '首次观察到已处理状态的时间',
  `first_seen_at` datetime(3) DEFAULT NULL COMMENT '首次登记时间',
  `last_seen_at` datetime(3) DEFAULT NULL COMMENT '最近出现时间',
  `ai_parsed_at` datetime(3) DEFAULT NULL COMMENT 'AI 解析完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  KEY `idx_group_join_requests_group_id` (`group_id`),
  KEY `idx_group_join_requests_user_id` (`user_id`),
  KEY `idx_group_join_requests_status` (`status`),
  KEY `idx_group_join_requests_last_seen_at` (`last_seen_at`),
  KEY `idx_group_join_requests_ai_parse_status` (`ai_parse_status`),
  KEY `idx_group_join_requests_group_decision_cursor` (`group_id`,`decision_status`,`last_seen_at`,`id`),
  KEY `idx_group_join_requests_observed_cursor` (`observed_status`,`last_seen_at`,`id`),
  KEY `idx_group_join_requests_user_cursor` (`user_id`,`last_seen_at`,`id`),
  KEY `fk_group_join_requests_last_decision` (`last_decision_id`,`id`),
  CONSTRAINT `fk_group_join_requests_last_decision` FOREIGN KEY (`last_decision_id`, `id`) REFERENCES `group_join_decisions` (`decision_id`, `request_id`),
  CONSTRAINT `chk_group_join_requests_ai_status` CHECK ((`ai_parse_status` in (_ascii'pending',_ascii'running',_ascii'succeeded',_ascii'failed',_ascii'skipped'))),
  CONSTRAINT `chk_group_join_requests_decision_source` CHECK (((`decision_source` is null) or (`decision_source` in (_ascii'manual',_ascii'automatic',_ascii'external')))),
  CONSTRAINT `chk_group_join_requests_decision_status` CHECK ((`decision_status` in (_ascii'pending',_ascii'processing',_ascii'approved',_ascii'rejected',_ascii'external_processed',_ascii'unknown'))),
  CONSTRAINT `chk_group_join_requests_legacy_status` CHECK ((`status` in (_ascii'pending',_ascii'processed'))),
  CONSTRAINT `chk_group_join_requests_observed_status` CHECK ((`observed_status` in (_ascii'pending',_ascii'checked'))),
  CONSTRAINT `chk_group_join_requests_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_group_join_requests_source` CHECK ((`source` in (_ascii'event',_ascii'system'))),
  CONSTRAINT `chk_group_join_requests_sub_type` CHECK ((`sub_type` in (_ascii'add',_ascii'invite')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `knowledge_trigger_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `source_key` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT 'WPS 词条稳定键',
  `trigger_type` varchar(32) NOT NULL COMMENT 'keyword_reply 或 ai_retrieval',
  `group_id` bigint NOT NULL COMMENT '触发所在 QQ 群',
  `triggered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_trigger_stats` (`triggered_at`,`source_key`,`trigger_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `managed_groups` (
  `group_id` bigint NOT NULL,
  `name` varchar(100) NOT NULL,
  `member_count` int unsigned NOT NULL DEFAULT '0',
  `max_member_count` int unsigned NOT NULL DEFAULT '0',
  `bot_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `snapshot_state` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `last_error_code` varchar(100) DEFAULT NULL,
  `last_error_message` varchar(300) DEFAULT NULL,
  `last_synced_at` datetime(3) DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `archived_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`group_id`),
  KEY `idx_managed_groups_state_cursor` (`snapshot_state`,`updated_at`,`group_id`),
  KEY `idx_managed_groups_archived_cursor` (`archived_at`,`updated_at`,`group_id`),
  CONSTRAINT `chk_managed_groups_bot_role` CHECK ((`bot_role` in (_utf8mb4'owner',_utf8mb4'admin',_utf8mb4'member',_utf8mb4'unknown'))),
  CONSTRAINT `chk_managed_groups_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_managed_groups_snapshot_state` CHECK ((`snapshot_state` in (_utf8mb4'fresh',_utf8mb4'stale')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `scheduled_job_runs` (
  `run_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `run_identity` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `job_id` bigint unsigned NOT NULL,
  `kind` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `scheduled_for` datetime(3) DEFAULT NULL,
  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  `duration_ms` int unsigned NOT NULL DEFAULT '0',
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
  KEY `idx_scheduled_job_runs_job_cursor` (`job_id`,`started_at`,`run_id`),
  KEY `idx_scheduled_job_runs_result_cursor` (`result`,`started_at`,`run_id`),
  KEY `idx_scheduled_job_runs_kind_cursor` (`kind`,`started_at`,`run_id`),
  KEY `idx_scheduled_job_runs_actor_cursor` (`triggered_by_user_id`,`started_at`,`run_id`),
  CONSTRAINT `fk_scheduled_job_runs_job` FOREIGN KEY (`job_id`) REFERENCES `scheduled_jobs` (`id`),
  CONSTRAINT `chk_scheduled_job_runs_actor_type` CHECK (((`triggered_by_type` is null) or (`triggered_by_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system')))),
  CONSTRAINT `chk_scheduled_job_runs_kind` CHECK ((`kind` in (_utf8mb4'scheduled',_utf8mb4'test'))),
  CONSTRAINT `chk_scheduled_job_runs_result` CHECK ((`result` in (_utf8mb4'success',_utf8mb4'failed',_utf8mb4'unknown',_utf8mb4'skipped')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `scheduled_jobs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(100) NOT NULL DEFAULT 'Scheduled job',
  `type` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '任务类型：每天/单次',
  `time_hhmm` varchar(5) NOT NULL COMMENT '触发时间，格式 HH:MM',
  `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL',
  `group_id` bigint NOT NULL COMMENT 'QQ群号',
  `message` text NOT NULL COMMENT '定时发送内容',
  `enabled` tinyint(1) NOT NULL COMMENT '是否启用',
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
  `timezone` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'Asia/Shanghai',
  `run_at` datetime(3) DEFAULT NULL,
  `last_run_at` datetime(3) DEFAULT NULL COMMENT '最近执行时间；用于防止同一天重复触发',
  `revision` int unsigned NOT NULL DEFAULT '1',
  `last_run_result` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `archived_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_scheduled_jobs_group_status_cursor` (`group_id`,`status`,`updated_at`,`id`),
  KEY `idx_scheduled_jobs_status_run_cursor` (`status`,`run_at`,`id`),
  CONSTRAINT `chk_scheduled_jobs_actor` CHECK ((((`updated_by_type` = _utf8mb4'system') and (`updated_by_user_id` is null) and (`updated_by_qq_user_id` is null)) or ((`updated_by_type` = _utf8mb4'admin_user') and (`updated_by_user_id` is not null)) or ((`updated_by_type` = _utf8mb4'qq_user') and (`updated_by_qq_user_id` is not null)))),
  CONSTRAINT `chk_scheduled_jobs_actor_role` CHECK (((`updated_by_role` is null) or (`updated_by_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_scheduled_jobs_actor_type` CHECK ((`updated_by_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_scheduled_jobs_last_run_result` CHECK (((`last_run_result` is null) or (`last_run_result` in (_utf8mb4'success',_utf8mb4'failed',_utf8mb4'unknown',_utf8mb4'skipped')))),
  CONSTRAINT `chk_scheduled_jobs_revision` CHECK ((`revision` >= 1)),
  CONSTRAINT `chk_scheduled_jobs_status` CHECK ((`status` in (_utf8mb4'active',_utf8mb4'paused',_utf8mb4'completed',_utf8mb4'archived'))),
  CONSTRAINT `chk_scheduled_jobs_type` CHECK ((`type` in (_utf8mb4'每天',_utf8mb4'单次')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `student_id_rules` (
  `rule_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `config_json` json NOT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `updated_by_display_name` varchar(100) NOT NULL,
  `updated_by_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`rule_id`),
  CONSTRAINT `chk_student_id_rules_actor_role` CHECK (((`updated_by_role` is null) or (`updated_by_role` in (_utf8mb4'super_admin',_utf8mb4'maintainer',_utf8mb4'observer')))),
  CONSTRAINT `chk_student_id_rules_actor_type` CHECK ((`updated_by_type` in (_utf8mb4'admin_user',_utf8mb4'qq_user',_utf8mb4'system'))),
  CONSTRAINT `chk_student_id_rules_config` CHECK ((json_type(`config_json`) = _utf8mb4'OBJECT')),
  CONSTRAINT `chk_student_id_rules_revision` CHECK ((`revision` >= 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
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
  KEY `idx_system_operations_status_cursor` (`status`,`requested_at`,`operation_id`),
  KEY `idx_system_operations_type_cursor` (`type`,`requested_at`,`operation_id`),
  KEY `idx_system_operations_idempotency` (`idempotency_id`),
  KEY `idx_system_operations_cleanup` (`completed_at`,`operation_id`),
  CONSTRAINT `fk_system_operations_idempotency` FOREIGN KEY (`idempotency_id`) REFERENCES `admin_idempotency_keys` (`idempotency_id`) ON DELETE SET NULL,
  CONSTRAINT `chk_system_operations_actor_type` CHECK ((`requested_by_type` in (_ascii'admin_user',_ascii'qq_user',_ascii'system'))),
  CONSTRAINT `chk_system_operations_completion` CHECK ((((`status` in (_ascii'accepted',_ascii'running')) and (`completed_at` is null)) or ((`status` in (_ascii'succeeded',_ascii'failed',_ascii'unknown')) and (`completed_at` is not null)))),
  CONSTRAINT `chk_system_operations_status` CHECK ((`status` in (_ascii'accepted',_ascii'running',_ascii'succeeded',_ascii'failed',_ascii'unknown'))),
  CONSTRAINT `chk_system_operations_type` CHECK ((`type` in (_utf8mb4'napcat_restart',_utf8mb4'knowledge_reload',_utf8mb4'bot_restart')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;

CREATE TABLE `schema_migrations` (
  `version` bigint unsigned NOT NULL,
  `name` varchar(255) NOT NULL,
  `applied_at` datetime(3) NOT NULL,
  PRIMARY KEY (`version`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE `join_approval_rule_state` (
  `rule_version` int unsigned NOT NULL,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `evidence_version` bigint unsigned NOT NULL DEFAULT '0',
  `activated_at` datetime(3) DEFAULT NULL,
  `rebuilt_at` datetime(3) DEFAULT NULL,
  `last_error_code` varchar(100) DEFAULT NULL,
  `revision` int unsigned NOT NULL DEFAULT '1',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`rule_version`),
  CONSTRAINT `chk_join_approval_rule_state_status` CHECK ((`status` in (_ascii'building',_ascii'ready',_ascii'failed'))),
  CONSTRAINT `chk_join_approval_rule_state_revision` CHECK ((`revision` >= 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE `join_major_code_samples` (
  `sample_id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `enrollment_year` char(4) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `major_code` char(3) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `major_name` varchar(128) NOT NULL,
  `normalized_major` varchar(128) NOT NULL,
  `source_request_id` bigint unsigned NOT NULL,
  `source_decision_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `approval_source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_group_id` bigint NOT NULL,
  `active` tinyint(1) NOT NULL DEFAULT '1',
  `revision` int unsigned NOT NULL DEFAULT '1',
  `corrected_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `corrected_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `corrected_by_display_name` varchar(100) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`sample_id`),
  UNIQUE KEY `uq_join_major_code_samples_request` (`source_request_id`),
  KEY `idx_join_major_code_samples_lookup` (`enrollment_year`,`major_code`,`active`,`normalized_major`),
  KEY `idx_join_major_code_samples_decision` (`source_decision_id`),
  CONSTRAINT `fk_join_major_code_samples_request` FOREIGN KEY (`source_request_id`) REFERENCES `group_join_requests` (`id`),
  CONSTRAINT `fk_join_major_code_samples_decision` FOREIGN KEY (`source_decision_id`) REFERENCES `group_join_decisions` (`decision_id`),
  CONSTRAINT `chk_join_major_code_samples_source` CHECK ((`approval_source` in (_ascii'manual',_ascii'automatic'))),
  CONSTRAINT `chk_join_major_code_samples_revision` CHECK ((`revision` >= 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE `admission_roster_versions` (
  `version_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `content_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `file_name` varchar(255) NOT NULL,
  `status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `row_count` int unsigned NOT NULL DEFAULT '0',
  `revision` int unsigned NOT NULL DEFAULT '1',
  `imported_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `imported_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `imported_by_display_name` varchar(100) NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `activated_at` datetime(3) DEFAULT NULL,
  `active_key` tinyint GENERATED ALWAYS AS ((case when (`status` = _ascii'active') then 1 else NULL end)) STORED,
  PRIMARY KEY (`version_id`),
  UNIQUE KEY `uq_admission_roster_versions_idempotency` (`idempotency_key`),
  UNIQUE KEY `uq_admission_roster_versions_active` (`active_key`),
  KEY `idx_admission_roster_versions_created` (`created_at`,`version_id`),
  CONSTRAINT `chk_admission_roster_versions_status` CHECK ((`status` in (_ascii'active',_ascii'superseded'))),
  CONSTRAINT `chk_admission_roster_versions_revision` CHECK ((`revision` >= 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE `admission_roster_entries` (
  `version_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `student_id` char(12) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `student_name` varchar(64) DEFAULT NULL,
  `major` varchar(128) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`version_id`,`student_id`),
  KEY `idx_admission_roster_entries_student` (`student_id`,`version_id`),
  CONSTRAINT `fk_admission_roster_entries_version` FOREIGN KEY (`version_id`) REFERENCES `admission_roster_versions` (`version_id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE `join_evidence_rebuild_operations` (
  `actor_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `result_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`actor_id`,`idempotency_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Baseline rows required by runtime serialization and validation.
INSERT INTO `feature_settings`
  (`setting_id`, `scope_type`, `group_id`, `settings_json`, `revision`, `updated_by_type`, `updated_by_user_id`, `updated_by_qq_user_id`, `updated_by_display_name`, `updated_by_role`)
VALUES
  ('settings_global', 'global', NULL, JSON_OBJECT(), 1, 'system', NULL, NULL, 'system', NULL);

INSERT INTO `student_id_rules`
  (`rule_id`, `config_json`, `revision`, `updated_by_type`, `updated_by_user_id`, `updated_by_qq_user_id`, `updated_by_display_name`, `updated_by_role`)
VALUES
  ('student_id_rule', JSON_OBJECT('enabled', FALSE, 'student_id_length', 12, 'enrollment_year_segment', NULL, 'major_code_segment', NULL, 'mappings', JSON_ARRAY()), 1, 'system', NULL, NULL, 'system', NULL);

INSERT INTO `join_approval_rule_state`
  (`rule_version`, `status`, `evidence_version`, `activated_at`, `rebuilt_at`, `last_error_code`, `revision`)
VALUES
  (2, 'building', 0, NULL, NULL, NULL, 1);

INSERT INTO `schema_migrations` (`version`, `name`, `applied_at`)
VALUES (2, '002_join_approval_v2.sql', CURRENT_TIMESTAMP(3));
/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;
