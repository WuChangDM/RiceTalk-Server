BEGIN;

-- DES-20261006-01 §二（E2EE 二期·零知识密钥协商）：
-- voice_rooms 增加房间口令两个字段。
--   * e2_ee_passphrase_enabled：房间是否启用口令派生模式（true=口令房）；
--   * e2_ee_magic：客户端上传的 magic 校验 blob（AES-GCM 密文的 base64），
--     服务端只保管密文、不接触口令与派生密钥。
-- 列名与 GORM 命名策略对齐（E2EEPassphraseEnabled → e2_ee_passphrase_enabled，
-- 与 000018 的 e2_ee_enabled 同一口径）；默认关闭，与一期服务端分发模式完全兼容。
ALTER TABLE voice_rooms ADD COLUMN IF NOT EXISTS e2_ee_passphrase_enabled BOOLEAN DEFAULT false;
ALTER TABLE voice_rooms ADD COLUMN IF NOT EXISTS e2_ee_magic TEXT DEFAULT '';

COMMIT;
