-- +goose Up
-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- TGPan 扩展：频道自动扫描配置
--
-- 一个频道一行。存「这个频道要多久扫一次、上次扫到哪了、结果如何」。
--
-- 增量扫描靠 last_message_id：只拉比它更新的消息，避免每轮全量翻历史
-- （全量翻历史极易触发 TG 限流 FLOOD_WAIT，严重会被封号）。
--
-- 字段类型说明：
--   channel_id      用 bigint，TG 频道 ID 会超过 int32
--   last_message_id 用 bigint，理由同上
--   folder_id       用 text，与 files.id 的类型保持一致
--                   （files.id 虽是 uuid，但 ORM 模型里映射为 string）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS teldrive.channel_scans (
    channel_id bigint NOT NULL PRIMARY KEY,
    user_id bigint NOT NULL,
    channel_name text DEFAULT '',
    folder_id text,
    enabled boolean NOT NULL DEFAULT true,
    interval_seconds int,
    last_message_id bigint NOT NULL DEFAULT 0,
    last_scan_at timestamptz,
    last_error text DEFAULT '',
    total_imported bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT timezone('utc'::text, now()),
    updated_at timestamptz NOT NULL DEFAULT timezone('utc'::text, now()),
    CONSTRAINT channel_scans_user_fkey FOREIGN KEY (user_id)
        REFERENCES teldrive.users(user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS channel_scans_enabled_idx
    ON teldrive.channel_scans (enabled);

CREATE INDEX IF NOT EXISTS channel_scans_user_idx
    ON teldrive.channel_scans (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS teldrive.channel_scans;
-- +goose StatementEnd
