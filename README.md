# WallLoop —— NAS 动态壁纸服务

WallLoop 是一个自托管的动态壁纸服务(类 Steam Wallpaper Engine 的 NAS 形态):
把大屏(电视/平板/手机浏览器)变成动态壁纸播放器,壁纸库与播单存在 NAS 上,
支持视频/图片、间隔轮换、日/夜时段切换,并对外提供 `now` 客户端 API。

**原创项目**(面板/播放器 UI 与后端均为原创,冲 TOS 应用中心原创档),Go 1.19 纯标准库单二进制,零第三方依赖。

## 功能

- **壁纸库**:上传视频(mp4/webm/mov/mkv)与图片(jpg/png/webp/gif),ffmpeg 自动探测时长/分辨率并生成缩略图;
- **播单引擎**:轮换(随机/顺序,1-1440 分钟)、日/夜时段(壁纸可标 日/夜/任 槽位,到点换挡)、单曲循环;轮换游标持久化,重启无损;
- **播放器**(`/play`):全屏双缓冲交叉淡入,视频循环/图片 Ken Burns 缓推,HUD 与光标自动隐藏,双击全屏;
- **演示壁纸生成**:面板一键用 NAS 本机 ffmpeg(lavfi)合成 mandelbrot 分形/生命游戏/测试图卡视频,或把上传图片拍成 Ken Burns 运镜——**安装包零版权素材**,内网可用;
- **客户端 API**:`GET /api/v1/now` 返回当前壁纸与媒体直链(Range 友好),供 Wallpaper Engine 移动版/Lively 等拉流;
- **中文优先管理面板**:原生 JS 无框架、全相对路径,天然兼容 TOS nginx 子路径 iframe 内嵌。

## 快速开始

```bash
./wallloop run
# 管理面 http://127.0.0.1:18241/   (面板 + 全部 API,经 TOS nginx /wallloop/ 反代)
# 公开面 http://<NAS>:18240/play   (播放器 + 媒体 + now API,电视/手机 LAN 直连)
```

双监听面设计:管理能力(上传/删除/设置)只绑 `127.0.0.1`,只能经 TOS 桌面悬浮窗
(TOS nginx、TOS 登录态之后)访问,天然免鉴权;公开面只读(播放器/媒体/now),
绑 `0.0.0.0` 供电视/平板/手机浏览器直连。

环境变量(见 `wallloop.env`):`WALLLOOP_LISTEN`(公开面,默认 `0.0.0.0:18240`)/
`WALLLOOP_ADMIN_LISTEN`(管理面,默认 `127.0.0.1:18241`)/ `WALLLOOP_DATA_DIR`
(默认 `/var/lib/wallloop`)/ `WALLLOOP_FFMPEG`(默认 `/usr/bin/ffmpeg`,TOS 出厂自带)/
`WALLLOOP_MAX_UPLOAD_BYTES`(默认 4 GB)。

## 目录结构

```
wallloop/
├── cmd/wallloop/          # CLI 入口(run / version)
├── internal/
│   ├── config/            # 环境变量配置
│   ├── library/           # 壁纸库 + 播单轮换引擎 + ffmpeg 集成 + 生成任务
│   └── api/               # HTTP 面(REST API / 媒体 Range 直出 / 面板 / 播放器)
│       └── webui/         # 原创前端(embed 进二进制,零构建)
├── smoke/smoke.sh         # 冒烟测试(对运行实例过核心 API)
└── dist.sh                # 交叉编译 linux/amd64 + linux/arm64 并出发布 tarball
```

## 构建

依赖:Go >= 1.19(构建不依赖任何 CGO/前端工具链)。

```bash
./dist.sh                       # 产出 dist/wallloop_<ver>_linux_amd64.tar.gz + arm64
./smoke/smoke.sh                # 对运行中的实例跑冒烟(需 ffmpeg + curl + python3)
```

## 数据与安全

- 数据目录:`<DATA_DIR>/library/<id>/src.<ext>` 每壁纸一目录,`state.json` 原子落盘(temp+rename);
- 上传限单文件 4 GB(`MaxBytesReader`),条目 id 为 8 位 crypto 随机十六进制,防路径穿越(所有文件操作强校验 id 格式);
- systemd 加固建议见 TOS 封装仓 tos-app-wallloop(NoNewPrivileges/ProtectSystem 等);
- 面板无独立鉴权:生产部署请只监听 `127.0.0.1` 并由 TOS nginx(TOS 登录态之后)反代暴露。

## 许可证

Apache-2.0(见 LICENSE)。
