-- system_request_id and flag represented the same NapCat identifier in two APIs.
-- A restart sees either the exact legacy pair or the exact completed absence; mixed states fail closed.
DROP PROCEDURE IF EXISTS `jxh_guard_007`;
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
END;
CALL `jxh_guard_007`();
DROP PROCEDURE `jxh_guard_007`;
