// wallloop —— TOS 原创应用:NAS 动态壁纸服务(类 Wallpaper Engine 的自托管形态)
//
// 子命令:
//
//	wallloop run      启动服务(壁纸库 + 播单轮换 + 管理面板 + 播放器,systemd 管理)
//	wallloop version  打印版本
package main

import (
	"fmt"
	"os"

	"github.com/ericbyz/wallloop/internal/api"
)

const version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `wallloop %s —— NAS 动态壁纸服务

用法:
  wallloop run          启动服务
  wallloop version      打印版本

环境变量:WALLLOOP_LISTEN / WALLLOOP_DATA_DIR / WALLLOOP_FFMPEG 可覆盖默认值。
播放器:http://<NAS>:18240/play   管理面板:http://<NAS>:18240/
客户端 API:http://<NAS>:18240/api/v1/now
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = api.Run()
	case "version", "-v", "--version":
		fmt.Println("wallloop", version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wallloop:", err)
		os.Exit(1)
	}
}
