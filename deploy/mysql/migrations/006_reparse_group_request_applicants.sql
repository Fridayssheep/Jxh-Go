-- 清理旧解析器从“学院+姓名+学号”等问题模板中误提取的加号。
-- 历史 add 申请中尚未成功解析的记录重新进入 AI 队列；invite 不包含申请人资料，不参与回填。
UPDATE `group_join_requests`
SET `student_name` = CASE
      WHEN TRIM(COALESCE(`student_name`, '')) IN ('+', '＋') THEN NULL
      ELSE `student_name`
    END,
    `major` = CASE
      WHEN TRIM(COALESCE(`major`, '')) IN ('+', '＋') THEN NULL
      ELSE `major`
    END,
    `ai_parse_status` = 'pending',
    `ai_parse_attempts` = 0,
    `ai_parsed_at` = NULL
WHERE `sub_type` = 'add'
  AND (
    `ai_parse_status` <> 'completed'
    OR TRIM(COALESCE(`student_name`, '')) IN ('+', '＋')
    OR TRIM(COALESCE(`major`, '')) IN ('+', '＋')
  );
