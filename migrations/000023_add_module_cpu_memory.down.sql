ALTER TABLE module_runtime_statuses
    DROP COLUMN IF EXISTS cpu,
    DROP COLUMN IF EXISTS memory;
