BEGIN;

-- DES-20261029-01（A9 二期）回滚：删除线程已读位表（索引随表删除）。
DROP TABLE IF EXISTS message_thread_reads;

COMMIT;
