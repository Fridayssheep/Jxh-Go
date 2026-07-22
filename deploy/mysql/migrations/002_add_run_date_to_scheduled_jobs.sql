ALTER TABLE `scheduled_jobs`
ADD COLUMN `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL'
AFTER `time_hhmm`;

UPDATE `scheduled_jobs`
SET `run_date` = CURRENT_DATE
WHERE `type` = '单次' AND `run_date` IS NULL AND `enabled` = TRUE;
