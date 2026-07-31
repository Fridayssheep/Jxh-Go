-- Bot restarts use the same durable operation and idempotency lifecycle as
-- other externally visible management actions.
DROP PROCEDURE IF EXISTS `jxh_extend_system_operations_012`;
CREATE PROCEDURE `jxh_extend_system_operations_012`()
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
    CHECK (`type` IN ('napcat_restart', 'knowledge_reload', 'bot_restart'));
END;
CALL `jxh_extend_system_operations_012`();
DROP PROCEDURE `jxh_extend_system_operations_012`;
