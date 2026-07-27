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
