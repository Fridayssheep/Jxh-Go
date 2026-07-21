-- Add run_date column to scheduled_jobs for single-run task scheduling
-- Migration for existing deployments

ALTER TABLE `scheduled_jobs`
ADD COLUMN `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL'
AFTER `time_hhmm`;
