// Package library 的 ffmpeg 集成:媒体探测、缩略图、演示壁纸现场生成。
//
// 版权红线:安装包不携带任何上游 CDN 素材;演示壁纸全部由 NAS 本机的
// ffmpeg(TOS 出厂自带)用 lavfi 合成源/用户上传图片现场编码产出。
package library

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Probe 用 ffmpeg -i 的 stderr 解析时长与分辨率(不依赖 ffprobe 是否随附)。
func Probe(ffmpeg, path string) (dur float64, w, h int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-i", path)
	out, _ := cmd.CombinedOutput() // -i 无输出文件,退出码非 0 属预期,信息在 stderr
	s := string(out)
	dur = parseDuration(s)
	w, h = parseResolution(s)
	return
}

// MakeThumb 生成 480px 宽缩略图:视频取第 1 秒帧,图片等比缩放。
func MakeThumb(ffmpeg, srcPath, thumbPath string, kind string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if kind == "video" {
		args = append(args, "-ss", "1")
	}
	args = append(args, "-i", srcPath, "-vframes", "1",
		"-vf", "scale=480:-2", "-q:v", "4", thumbPath)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("缩略图生成失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// GenPreset 内置生成器预设(面板"演示壁纸"入口,全部本地合成零版权素材)。
type GenPreset struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// GenPresets 预设清单(前端按此渲染,不重复维护)。
var GenPresets = []GenPreset{
	{Kind: "mandelbrot", Label: "曼德博巡游", Detail: "lavfi mandelbrot 分形缓缩放,紫蓝梦境风"},
	{Kind: "life", Label: "生命游戏", Detail: "lavfi life 元胞自动机演化,极客暗色系"},
	{Kind: "testsrc", Label: "测试图卡", Detail: "lavfi testsrc2 彩条游标,校准器风格"},
	{Kind: "kenburns", Label: "照片变动态", Detail: "把选中的一张图片用 zoompan 拍成 Ken Burns 运镜视频"},
}

// GenerateArgs 按预设构造 ffmpeg 参数(输出 720p25 yuv420p,NAS CPU 数分钟内可完成)。
// srcPath 仅 kenburns 需要(取上传图片做运动)。
func GenerateArgs(ffmpeg, preset string, seconds int, srcPath, outPath string) ([]string, error) {
	if seconds < 5 || seconds > 120 {
		return nil, fmt.Errorf("时长须在 5-120 秒")
	}
	var filter string
	switch preset {
	case "mandelbrot":
		filter = fmt.Sprintf("mandelbrot=s=1280x720:r=25:end_scale=%f", 900.0+float64(seconds)*18)
	case "life":
		filter = "life=s=1280x720:r=25:mold=10:ratio=0.08:death_color=#2A2140:life_color=#7FD1FF:mold_color=#120B1E"
	case "testsrc":
		filter = "testsrc2=s=1280x720:r=25"
	case "kenburns":
		if srcPath == "" {
			return nil, fmt.Errorf("kenburns 需要选择一张图片壁纸")
		}
		// 先放大再做缓推拉(经典 Ken Burns:center → 1.18x)
		filter = fmt.Sprintf("scale=2560:-2,zoompan=z='min(zoom+0.0006,1.18)':d=%d:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':s=1280x720:fps=25", seconds*25)
	default:
		return nil, fmt.Errorf("未知预设: %s", preset)
	}
	args := []string{ffmpeg, "-hide_banner", "-loglevel", "error", "-y"}
	if srcPath != "" {
		args = append(args, "-loop", "1", "-i", srcPath)
	} else {
		args = append(args, "-f", "lavfi", "-i", filter)
	}
	if preset == "kenburns" {
		args = append(args, "-vf", filter)
	}
	args = append(args,
		"-t", strconv.Itoa(seconds),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart",
		outPath,
	)
	return args, nil
}

// —— ffmpeg stderr 解析 ——

var (
	durRe = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	resRe = regexp.MustCompile(`, (\d{2,5})x(\d{2,5})[ ,]`)
)

func parseDuration(out string) float64 {
	m := durRe.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mn, _ := strconv.Atoi(m[2])
	s, _ := strconv.ParseFloat(m[3], 64)
	return float64(h)*3600 + float64(mn)*60 + s
}

func parseResolution(out string) (int, int) {
	m := resRe.FindStringSubmatch(out)
	if m == nil {
		return 0, 0
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	return w, h
}

// NewID 生成 8 位小写十六进制条目 id(crypto 随机,防遍历/防碰撞)。
func NewID() string {
	b := make([]byte, 4)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return fmt.Sprintf("%08x", b)
}
