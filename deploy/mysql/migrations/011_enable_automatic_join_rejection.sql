-- Permit the existing per-group policy flag to reject AI-validated invalid applications.
-- Existing rows remain false; enabling rejection always requires an explicit policy update.
ALTER TABLE `group_join_policies`
  DROP CHECK `chk_group_join_policies_auto_reject`;
