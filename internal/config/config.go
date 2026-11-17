// Package config 提供 wallloop 的运行配置:环境变量 + 缺省值(无配置文件,
// 面板内改设置落 state.json,与 AdWall 的 yaml 方案相比更轻)。
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 服务运行配置(全部可用环境变量覆盖,见 wallloop.env)。
type Config struct {
	Listen        string // 公开面监听(播放器/媒体/now API,0.0.0.0 给电视/手机直连)
	Port          string // 公开面端口备案值(须与 Listen 端口一致)
	AdminListen   string // 管理面监听(面板 + 写 API,127.0.0.1 只给 TOS nginx 反代)
	AdminPort     string // 管理面端口备案值
	AppID         string // 应用 id(nginx 路由前缀,日志/文案用)
	DataDir       string // 壁纸库/state.json/缩略图落盘目录
	FFmpegPath    string // ffmpeg 可执行文件路径(TOS 出厂自带 /usr/bin/ffmpeg)
	MaxUploadByte int64  // 单文件上传上限
}

// Load 从环境变量构造配置,缺省面向 TOS 部署(/var/lib/wallloop)。
func Load() *Config {
	c := &Config{
		Listen:        envOr("WALLLOOP_LISTEN", "0.0.0.0:18240"),
		Port:          envOr("WALLLOOP_PORT", "18240"),
		AdminListen:   envOr("WALLLOOP_ADMIN_LISTEN", "127.0.0.1:18241"),
		AdminPort:     envOr("WALLLOOP_ADMIN_PORT", "18241"),
		AppID:         envOr("WALLLOOP_APP_ID", "wallloop"),
		DataDir:       envOr("WALLLOOP_DATA_DIR", "/var/lib/wallloop"),
		FFmpegPath:    envOr("WALLLOOP_FFMPEG", "/usr/bin/ffmpeg"),
		MaxUploadByte: envIntOr("WALLLOOP_MAX_UPLOAD_BYTES", 4<<30),
	}
	// 本机开发兜底:数据目录建不了就退到临时目录,便于无 root 环境跑通
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		c.DataDir = filepath.Join(os.TempDir(), "wallloop")
		_ = os.MkdirAll(c.DataDir, 0o755)
	}
	return c
}

// LibraryDir 壁纸文件根目录(每个壁纸一个子目录:<id>/src.<ext> + thumb.jpg)。
func (c *Config) LibraryDir() string { return filepath.Join(c.DataDir, "library") }

// StatePath state.json(壁纸清单 + 播单设置 + 轮换游标)。
func (c *Config) StatePath() string { return filepath.Join(c.DataDir, "state.json") }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}
