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
	ID       string `gorm:"type:uuid;primaryKey;default:uuid7()"`
	UserId   int64  `gorm:"type:bigint;not null;index"`
	// Username 播放器里填的用户名，默认用 TG 用户 ID 的字符串形式
	Username string `gorm:"type:text;not null"`
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
	LastMessageID int     `gorm:"type:bigint;not null;default:0"`
	LastScanAt    *time.Time
	LastError     string `gorm:"type:text"`
	TotalImported int64  `gorm:"type:bigint;not null;default:0"`
	CreatedAt     time.Time `gorm:"default:timezone('utc'::text, now())"`
	UpdatedAt     time.Time `gorm:"default:timezone('utc'::text, now())"`
}

func (ChannelScan) TableName() string { return "channel_scans" }

