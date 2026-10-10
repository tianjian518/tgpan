-- +goose Up
-- v12：深度扫描（翻老片）+ 扫描性能保护
--
-- 1) channel_scans 加"深扫水位"列：深度扫描已翻到的最旧消息 ID。
--    与 last_message_id（增量游标，只往大推）方向相反，只往小推。
--    0 表示还没深扫过。
ALTER TABLE teldrive.channel_scans ADD COLUMN IF NOT EXISTS deepest_message_id bigint NOT NULL DEFAULT 0;

-- 2) files 按频道查询的索引。
--    扫描去重、增量扫描、频道文件列表全都按 channel_id 过滤，
--    文件量上万之后没有索引就是全表扫描，N1 这种小机器会被拖慢。
CREATE INDEX IF NOT EXISTS idx_files_channel_user ON teldrive.files (channel_id, user_id);

-- +goose Down
DROP INDEX IF EXISTS teldrive.idx_files_channel_user;
ALTER TABLE teldrive.channel_scans DROP COLUMN IF EXISTS deepest_message_id;
