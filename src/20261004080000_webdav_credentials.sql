-- +goose Up
-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- TGPan 扩展：WebDAV 凭据表
-- 给播放器（网易爆米花 / Infuse / VidHub / nPlayer）挂载用
--
-- 注意：
--   1. id 用 public.uuid7()，与项目现有文件表保持一致
--      （旧版 teldrive.generate_uid 已在 20240802 的迁移中被移除）
--   2. user_id 外键指向 teldrive.users(user_id)，级联删除
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS teldrive.webdav_credentials (
    id uuid NOT NULL DEFAULT public.uuid7() PRIMARY KEY,
    user_id bigint NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    label text DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    last_used timestamptz,
    created_at timestamptz NOT NULL DEFAULT timezone('utc'::text, now()),
    updated_at timestamptz NOT NULL DEFAULT timezone('utc'::text, now()),
    CONSTRAINT webdav_credentials_user_fkey FOREIGN KEY (user_id)
        REFERENCES teldrive.users(user_id) ON DELETE CASCADE,
    CONSTRAINT webdav_credentials_username_un UNIQUE (username)
);

CREATE INDEX IF NOT EXISTS webdav_credentials_user_id_idx
    ON teldrive.webdav_credentials (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS teldrive.webdav_credentials;
-- +goose StatementEnd
