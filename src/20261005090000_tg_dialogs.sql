-- +goose Up
-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- TGPan 扩展：已关注频道缓存（tg_dialogs）
--
-- 为什么需要这张表：
--
--   用户要扫频道，得先知道有哪些频道。让人手填 TG 频道 ID 是反人类的 ——
--   大多数人根本不知道自己的频道 ID 是多少，得点进消息链接里数那串数字，
--   还要判断加不加 -100 前缀。而账号本来就关注了这些频道，
--   直接列出来点一下最省事。
--
--   但拉对话列表（messages.getDialogs）只能走真实 TG 会话，一次调用要
--   几百毫秒到几秒，还可能吃限流。放在 HTTP 请求里同步拉，用户会看到
--   一个转圈圈转很久的列表。所以改成后台定时同步 + 这张表做缓存，
--   接口只读库，毫秒级返回。
--
-- 字段说明：
--   channel_id   用 bigint，TG 频道 ID 会超过 int32
--   access_hash  拿到后可以直接构造 InputPeer，省掉每次 ChannelsGetChannels
--                的往返（对话列表里本来就带，白扔可惜）
--   is_channel   区分「频道」和「群组」。群组默认不列在扫描页里 ——
--                群聊里很少有人发影视资源，混进去只会让列表变长。
--   synced_at    每次同步覆盖写，前端可以显示「多久之前同步的」
--
-- 主键用 (user_id, channel_id)：同一个频道被多个账号关注是正常的，
-- 各记各的，互不覆盖。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS teldrive.tg_dialogs (
    user_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    access_hash bigint NOT NULL DEFAULT 0,
    title text DEFAULT '',
    username text DEFAULT '',
    is_channel boolean NOT NULL DEFAULT true,
    member_count int DEFAULT 0,
    synced_at timestamptz NOT NULL DEFAULT timezone('utc'::text, now()),
    CONSTRAINT tg_dialogs_pkey PRIMARY KEY (user_id, channel_id),
    CONSTRAINT tg_dialogs_user_fkey FOREIGN KEY (user_id)
        REFERENCES teldrive.users(user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS tg_dialogs_user_idx
    ON teldrive.tg_dialogs (user_id, is_channel);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS teldrive.tg_dialogs;
-- +goose StatementEnd
