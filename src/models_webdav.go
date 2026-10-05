package models

import (
	"time"
)

// WebDAVCredential 是给播放器（网易爆米花 / Infuse / VidHub / nPlayer 等）
// 挂载 WebDAV 用的独立凭据。
//
// 为什么单独存而不是复用登录 Cookie：
//   - 播放器只支持 HTTP Basic Auth，不支持浏览器 Cookie
//   - 播放器里填的密码要长期有效，不宜跟网页登录会话绑定
//   - 泄漏了可以单独撤销，不影响网页登录
type WebDAVCredential struct {
	ID     string `gorm:"type:uuid;primaryKey;default:uuid7()"`
	UserId int64  `gorm:"type:bigint;not null;index"`
	// Username 播放器里填的用户名，默认用 TG 用户 ID 的字符串形式。
	// 必须唯一：认证时是按 username 查一条来比对密码的，
	// 一旦出现重复用户名，查出来的可能是另一个人的记录，
	// 表现为「密码明明对却登不上」或者更糟——匹配到别人的账号。
	Username string `gorm:"type:text;not null;uniqueIndex:idx_webdav_username"`
	// PasswordHash 用 bcrypt 存密码哈希，不存明文
	PasswordHash string `gorm:"type:text;not null"`
	// Label 备注名，方便识别是给哪台设备生成的
	Label     string     `gorm:"type:text"`
	Enabled   bool       `gorm:"type:bool;default:true"`
	LastUsed  *time.Time `gorm:"type:timestamptz"`
	CreatedAt time.Time  `gorm:"default:timezone('utc'::text, now())"`
	UpdatedAt time.Time  `gorm:"default:timezone('utc'::text, now())"`
}

func (WebDAVCredential) TableName() string { return "webdav_credentials" }

// ChannelScan 是频道自动扫描的配置与游标。
//
// 一个 TG 频道对应一行。核心字段是 LastMessageID，它是增量扫描的游标：
// 每次只拉比它更新的消息，扫完把游标推到最新。这样既快又不会触发 TG 限流。
type ChannelScan struct {
	ChannelId   int64  `gorm:"type:bigint;primaryKey"`
	UserId      int64  `gorm:"type:bigint;not null;index"`
	ChannelName string `gorm:"type:text"`
	// FolderID 网盘里这个频道对应的顶层文件夹（一频道一文件夹）
	FolderID string `gorm:"type:text"`
	// Enabled 是否参与自动扫描
	Enabled bool `gorm:"type:bool;not null;default:true"`
	// IntervalSeconds 单独指定扫描间隔（秒）；nil 表示用全局默认
	IntervalSeconds *int `gorm:"type:int"`
	// LastMessageID 上次扫到的最大消息 id，增量扫描游标
	LastMessageID int `gorm:"type:bigint;not null;default:0"`
	LastScanAt    *time.Time
	LastError     string    `gorm:"type:text"`
	TotalImported int64     `gorm:"type:bigint;not null;default:0"`
	CreatedAt     time.Time `gorm:"default:timezone('utc'::text, now())"`
	UpdatedAt     time.Time `gorm:"default:timezone('utc'::text, now())"`
}

func (ChannelScan) TableName() string { return "channel_scans" }

// TGDialog 是「这个 TG 账号关注了哪些频道」的本地缓存。
//
// 为什么要有这张表：
//
//	扫描页要让用户直接从已关注的频道里挑，而不是手填 ID。但拉对话列表
//	只能走真实 TG 会话（messages.getDialogs），一次要几百毫秒到几秒，
//	还可能吃限流。放在 HTTP 请求里同步拉，用户会一直盯着转圈。
//
// 所以后台定时同步进这张表，接口只读库，毫秒级返回。
type TGDialog struct {
	// UserId + ChannelId 是复合主键：同一个频道被多个账号关注是正常的，
	// 各记各的，互不覆盖。
	UserId    int64 `gorm:"type:bigint;primaryKey"`
	ChannelId int64 `gorm:"type:bigint;primaryKey"`
	// AccessHash 拿到后可以直接构造 InputPeer，
	// 省掉每次 ChannelsGetChannels 的一次往返 —— 对话列表本来就带了，白扔可惜。
	AccessHash int64  `gorm:"type:bigint;not null;default:0"`
	Title      string `gorm:"type:text"`
	Username   string `gorm:"type:text"`
	// IsChannel 区分「频道」和「群组」。群组默认不列在扫描页 ——
	// 群聊里很少有人发影视资源，混进去只会让列表变长。
	IsChannel   bool      `gorm:"type:bool;not null;default:true"`
	MemberCount int       `gorm:"type:int;default:0"`
	SyncedAt    time.Time `gorm:"default:timezone('utc'::text, now())"`
}

func (TGDialog) TableName() string { return "tg_dialogs" }
