-- 没有 flag 的旧记录无法对应 NapCat 群通知，不能参与后续去重或处理。
DELETE FROM `group_join_requests`
WHERE `flag` IS NULL OR `flag` = '';

ALTER TABLE `group_join_requests`
  DROP INDEX `idx_group_join_requests_request_key`,
  MODIFY COLUMN `flag` varchar(512) NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串',
  ADD UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  DROP COLUMN `request_key`;
