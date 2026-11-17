// Package api 装配壁纸库与 HTTP 面:REST API + 媒体服务(支持 Range)+ 管理面板 + 播放器。
//
// 路径约定(全相对,兼容 TOS nginx /wallloop/ 前缀剥离的 iframe 内嵌,同 AdWall):
//   - 面板与播放器页面里的资源/API/媒体一律用相对路径,由前端按 document.baseURI 推 BASE;
//   - API 返回的媒体地址也是相对形式(media/<id>/...),由前端拼 BASE。
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ericbyz/wallloop/internal/config"
	"github.com/ericbyz/wallloop/internal/library"
)

//go:embed webui
var webFS embed.FS

type server struct {
	cfg  *config.Config
	lib  *library.Library
	jobs *library.Jobs
}

// Run 启动服务(阻塞,由 systemd 管理生命周期)。
//
// 双监听面设计(同 AdWall 的"面板仅 TOS 桌面可达"叙事,扩展到播放器场景):
//   - 管理面 ADMIN_LISTEN(默认 127.0.0.1:18241):面板 + 全部 API——只能经
//     TOS nginx /wallloop/(TOS 登录态之后)反代访问,天然免鉴权;
//   - 公开面 LISTEN(默认 0.0.0.0:18240):只读子集(播放器页/媒体 Range/
//     now/playlist)——电视、平板、手机浏览器直连,即动态壁纸大屏本体。
func Run() error {
	cfg := config.Load()
	lib, err := library.Open(cfg.StatePath(), cfg.LibraryDir())
	if err != nil {
		return fmt.Errorf("打开壁纸库: %w", err)
	}
	s := &server{cfg: cfg, lib: lib, jobs: library.NewJobs()}

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	adminMux.HandleFunc("/api/v1/state", s.handleState)
	adminMux.HandleFunc("/api/v1/upload", s.handleUpload)
	adminMux.HandleFunc("/api/v1/items/", s.handleItem)
	adminMux.HandleFunc("/api/v1/settings", s.handleSettings)
	adminMux.HandleFunc("/api/v1/generate", s.handleGenerate)
	adminMux.HandleFunc("/api/v1/jobs", s.handleJobs)
	adminMux.HandleFunc("/api/v1/now", s.handleNow)
	adminMux.HandleFunc("/api/v1/playlist", s.handlePlaylist)
	adminMux.HandleFunc("/media/", s.handleMedia)
	adminMux.HandleFunc("/play", s.handlePage("play.html"))
	adminMux.HandleFunc("/", s.handleRoot)

	// 公开面:只读子集(播放器 + 媒体 + now/playlist + 健康检查)
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	publicMux.HandleFunc("/api/v1/now", s.handleNow)
	publicMux.HandleFunc("/api/v1/playlist", s.handlePlaylist)
	publicMux.HandleFunc("/media/", s.handleMedia)
	publicMux.HandleFunc("/play", s.handlePage("play.html"))
	publicMux.HandleFunc("/logo.svg", s.handleAsset("logo.svg"))
	publicMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 其余一律 404:上传/删除/设置等管理能力不暴露到 LAN
		http.NotFound(w, r)
	})

	adminSrv := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           logReq(adminMux),
		ReadHeaderTimeout: 30 * time.Second,
	}
	publicSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           logReq(publicMux),
		ReadHeaderTimeout: 30 * time.Second,
		// 大视频下载不设整体读超时(默认无限),靠连接关闭结束
	}

	errc := make(chan error, 2)
	go func() {
		log.Printf("wallloop: 管理面 http://%s/(面板,经 TOS nginx /%s/ 反代)", cfg.AdminListen, cfg.AppID)
		errc <- adminSrv.ListenAndServe()
	}()
	go func() {
		log.Printf("wallloop: 公开面 http://%s/play(播放器,LAN 直连)", cfg.Listen)
		errc <- publicSrv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		return err
	case <-stop:
		log.Printf("wallloop: 收到退出信号,优雅关闭")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = adminSrv.Shutdown(ctx)
		_ = publicSrv.Shutdown(ctx)
		return nil
	}
}

// handleAsset 出 embed 内单个静态文件(公开面播放器引用 logo 用)。
func (s *server) handleAsset(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		static, _ := fs.Sub(webFS, "webui")
		s.serveFile(w, r, static, name)
	}
}

// —— 页面 ——

// handleRoot:/ 与 /index.html 出面板;子路径静态资源(embed FS,相对路径)。
func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	static, err := fs.Sub(webFS, "webui")
	if err != nil {
		http.Error(w, "面板资源缺失", http.StatusInternalServerError)
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		s.serveFile(w, r, static, "index.html")
		return
	}
	// /logo.svg /style.css 等静态资源(path.Clean 用 URL 语义,embed FS 是 / 分隔,
	// Windows 下 filepath.Clean 会给反斜杠导致 404)
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if f, err := static.Open(name); err == nil {
		f.Close()
		s.serveFile(w, r, static, name)
		return
	}
	http.NotFound(w, r)
}

func (s *server) handlePage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		static, _ := fs.Sub(webFS, "webui")
		s.serveFile(w, r, static, name)
	}
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "资源不可读", http.StatusInternalServerError)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// —— JSON 工具 —

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	return nil
}

// —— API —

// publicItem 面板/客户端共用的条目视图(媒体地址相对路径,由前端拼 BASE)。
func publicItem(it library.Item) map[string]interface{} {
	m := map[string]interface{}{
		"id": it.ID, "name": it.Name, "kind": it.Kind, "ext": it.Ext,
		"bytes": it.Bytes, "slot": it.Slot, "active": it.Active,
		"generated": it.Generated, "createdAt": it.CreatedAt,
		"url":   fmt.Sprintf("media/%s/src.%s", it.ID, it.Ext),
		"thumb": fmt.Sprintf("media/%s/thumb.jpg", it.ID),
	}
	if it.DurationSec > 0 {
		m["durationSec"] = it.DurationSec
	}
	if it.Width > 0 {
		m["width"], m["height"] = it.Width, it.Height
	}
	return m
}

// handleState GET /api/v1/state:面板一次性拉全量(条目+设置+当前+任务+概览)。
func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	items, set, cur := s.lib.Snapshot(time.Now())
	curMap := map[string]interface{}{}
	if cur != nil {
		curMap = publicItem(*cur)
	}
	views := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		views = append(views, publicItem(it))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": views, "settings": set, "current": curMap,
		"stats": s.lib.SnapshotStats(), "jobs": s.jobs.Snapshot(),
		"presets":    library.GenPresets,
		"publicPort": s.cfg.Port,
	})
}

// handleUpload POST /api/v1/upload?name=展示名 multipart 字段 file。
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadByte)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("解析上传失败(超过上限 %d GB?): %w", s.cfg.MaxUploadByte>>30, err))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		errJSON(w, http.StatusBadRequest, errors.New("缺少 file 字段"))
		return
	}
	defer file.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(hdr.Filename), "."))
	kind, ok := kindOf(ext)
	if !ok {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("不支持的格式 .%s(支持 mp4/webm/mov/mkv/jpg/png/webp/gif)", ext))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename))
	}
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}

	id := library.NewID()
	dir := filepath.Join(s.cfg.LibraryDir(), id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		errJSON(w, http.StatusInternalServerError, err)
		return
	}
	srcPath := filepath.Join(dir, "src."+ext)
	dst, err := os.OpenFile(srcPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		os.RemoveAll(dir)
		errJSON(w, http.StatusInternalServerError, err)
		return
	}
	written, err := io.Copy(dst, file)
	closeErr := dst.Close()
	if err != nil || closeErr != nil {
		os.RemoveAll(dir)
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("落盘失败: %v", err))
		return
	}
	if written == 0 {
		os.RemoveAll(dir)
		errJSON(w, http.StatusBadRequest, errors.New("空文件"))
		return
	}

	// 探测 + 缩略图(视频取第 1 秒帧;失败不阻断入库,面板有无缩略图兜底样式)
	dur, vw, vh := 0.0, 0, 0
	if kind == "video" {
		dur, vw, vh = library.Probe(s.cfg.FFmpegPath, srcPath)
	} else {
		_, vw, vh = library.Probe(s.cfg.FFmpegPath, srcPath) // 图片只要分辨率
	}
	_ = library.MakeThumb(s.cfg.FFmpegPath, srcPath, filepath.Join(dir, "thumb.jpg"), kind)

	it := library.Item{ID: id, Name: name, Kind: kind, Ext: ext, Bytes: written,
		DurationSec: dur, Width: vw, Height: vh, Slot: "any", Active: true, CreatedAt: time.Now()}
	if err := s.lib.Add(it); err != nil {
		os.RemoveAll(dir)
		errJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, publicItem(it))
}

func kindOf(ext string) (string, bool) {
	switch ext {
	case "mp4", "webm", "mov", "mkv":
		return "video", true
	case "jpg", "jpeg", "png", "webp", "gif":
		return "image", true
	}
	return "", false
}

// handleItem /api/v1/items/{id}[/{action}]:
//
//	DELETE            删除
//	POST rename       {name}
//	POST slot         {slot: any|day|night}
//	POST active       {active: bool}
//	POST activate     立即播放
func (s *server) handleItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/items/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		errJSON(w, http.StatusNotFound, errors.New("缺少条目 id"))
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	var err error
	switch {
	case r.Method == http.MethodDelete && action == "":
		err = s.lib.Delete(id)
	case r.Method == http.MethodPost && action == "rename":
		var body struct{ Name string }
		if err = readJSON(r, &body); err == nil {
			err = s.lib.Update(id, &body.Name, nil, nil)
		}
	case r.Method == http.MethodPost && action == "slot":
		var body struct{ Slot string }
		if err = readJSON(r, &body); err == nil {
			err = s.lib.Update(id, nil, &body.Slot, nil)
		}
	case r.Method == http.MethodPost && action == "active":
		var body struct{ Active bool }
		if err = readJSON(r, &body); err == nil {
			err = s.lib.Update(id, nil, nil, &body.Active)
		}
	case r.Method == http.MethodPost && action == "activate":
		err = s.lib.Activate(id)
	default:
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("不支持 %s %s", r.Method, r.URL.Path))
		return
	}
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			errJSON(w, http.StatusNotFound, err)
		} else {
			errJSON(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// handleSettings POST /api/v1/settings(整体提交,热生效)。
func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var set library.Settings
	if err := readJSON(r, &set); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := s.lib.SetSettings(set); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// handleGenerate POST /api/v1/generate {kind, seconds, srcId?} → {jobId}。
func (s *server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		Seconds int    `json:"seconds"`
		SrcID   string `json:"srcId"`
	}
	if err := readJSON(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	srcPath := ""
	if body.Kind == "kenburns" {
		if body.SrcID == "" {
			errJSON(w, http.StatusBadRequest, errors.New("kenburns 需要选择一张图片壁纸"))
			return
		}
		it, p, err := s.lib.SrcPath(body.SrcID)
		if err != nil {
			errJSON(w, http.StatusNotFound, errors.New("源图片不存在"))
			return
		}
		if it.Kind != "image" {
			errJSON(w, http.StatusBadRequest, errors.New("kenburns 只能选图片壁纸"))
			return
		}
		srcPath = p
	}
	job, err := library.StartGen(s.jobs, s.lib, s.cfg.FFmpegPath, s.cfg.LibraryDir(), body.Kind, body.Seconds, srcPath)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"jobId": job.ID})
}

// handleJobs GET /api/v1/jobs:生成任务列表(面板轮询)。
func (s *server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": s.jobs.Snapshot()})
}

// handleNow GET /api/v1/now:客户端 API(播放器/Wallpaper Engine 移动端/Lively 拉流)。
// 短缓存:轮换间隔内多端拉取不重算;播放器按 pollSec 提示做下一轮检查。
func (s *server) handleNow(w http.ResponseWriter, r *http.Request) {
	_, set, cur := s.lib.Snapshot(time.Now())
	w.Header().Set("Cache-Control", "public, max-age=15")
	if cur == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": nil, "mode": set.Mode, "pollSec": 30})
		return
	}
	pollSec := 30
	if set.Mode == "single" {
		pollSec = 300 // 单曲模式几乎不变,长轮询间隔
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"item": publicItem(*cur), "mode": set.Mode, "pollSec": pollSec})
}

// handlePlaylist GET /api/v1/playlist:激活播单(客户端批量拉取)。
func (s *server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	items, set, _ := s.lib.Snapshot(time.Now())
	views := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		if it.Active {
			views = append(views, publicItem(it))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": views, "settings": set})
}

// handleMedia GET /media/{id}/(src.<ext>|thumb.jpg):Range 友好的媒体直出。
func (s *server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("GET/HEAD only"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/media/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || !idOK(parts[0]) {
		http.NotFound(w, r)
		return
	}
	id, file := parts[0], parts[1]

	var path string
	switch file {
	case "thumb.jpg":
		p, err := s.lib.ThumbPath(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path = p
		w.Header().Set("Cache-Control", "public, max-age=3600")
	default:
		it, p, err := s.lib.SrcPath(id)
		if err != nil || file != "src."+it.Ext {
			http.NotFound(w, r)
			return
		}
		path = p
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Header().Set("Accept-Ranges", "bytes")
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// http.ServeContent 自带 Range/If-Modified-Since(视频拖动进度条必需)
	http.ServeContent(w, r, filepath.Base(path), time.Time{}, f)
}

func idOK(id string) bool {
	if len(id) != 8 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// logReq 访问日志(静音媒体轮询噪音:同一连接的 Range 续传高频)。
func logReq(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/media/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}
