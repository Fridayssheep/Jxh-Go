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
