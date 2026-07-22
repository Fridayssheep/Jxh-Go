ALTER TABLE `group_join_requests`
MODIFY COLUMN `flag` mediumtext DEFAULT NULL COMMENT '处理群申请时需要的原始 flag';
