-- Add cpu/memory columns to module_runtime_statuses
-- These fields were added to model.ModuleRuntimeStatus but not to the baseline schema.
ALTER TABLE module_runtime_statuses
    ADD COLUMN IF NOT EXISTS cpu DOUBLE PRECISION DEFAULT 0,
    ADD COLUMN IF NOT EXISTS memory DOUBLE PRECISION DEFAULT 0;
