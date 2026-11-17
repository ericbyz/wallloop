// Package library 管理壁纸库:条目 CRUD、state.json 原子落盘、播单轮换引擎。
//
// 设计要点:
//   - 无数据库:单 JSON 状态文件 + 每壁纸一个目录(library/<id>/src.<ext>),
//     原子写(temp+rename),断电安全;
//   - 轮换无后台协程:每次取"当前壁纸"时按 时钟 + 轮换游标 现场推导并按需推进,
//     服务重启后状态无损;
//   - 三种模式:single(单曲)/ rotation(间隔轮换)/ timeofday(日/夜时段)。
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item 一条壁纸(视频或图片)。
type Item struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"` // video | image
	Ext         string    `json:"ext"`  // mp4/webm/mov/mkv | jpg/jpeg/png/webp/gif
	Bytes       int64     `json:"bytes"`
	DurationSec float64   `json:"durationSec,omitempty"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	Slot        string    `json:"slot"`                // any | day | night(timeofday 模式用)
	Active      bool      `json:"active"`              // 是否进入播单
	Generated   bool      `json:"generated,omitempty"` // 由内置生成器产出
	CreatedAt   time.Time `json:"createdAt"`
}

// Settings 播单设置(面板可改,热生效)。
type Settings struct {
	Mode        string `json:"mode"`        // single | rotation | timeofday
	IntervalMin int    `json:"intervalMin"` // rotation 轮换间隔(分钟)
	Order       string `json:"order"`       // seq | shuffle
	DayStart    string `json:"dayStart"`    // timeofday 日间起点 "07:00"
	NightStart  string `json:"nightStart"`  // timeofday 夜间起点 "19:00"
}

func defaultSettings() Settings {
	return Settings{Mode: "rotation", IntervalMin: 30, Order: "shuffle", DayStart: "07:00", NightStart: "19:00"}
}

// State state.json 顶层结构。
type State struct {
	Version  int      `json:"version"`
	Items    []Item   `json:"items"`
	Settings Settings `json:"settings"`
	Rotation rotation `json:"rotation"`
}

type rotation struct {
	CurrentID  string    `json:"currentId"`
	LastSwitch time.Time `json:"lastSwitch"`
}

// ErrNotFound 操作的条目不存在。
var ErrNotFound = errors.New("item not found")

var idRe = regexp.MustCompile(`^[a-z0-9]{8}$`)

// Library 并发安全的壁纸库。
type Library struct {
	mu    sync.Mutex
	state *State
	path  string // state.json 路径
	dir   string // library/ 根目录
}

// Open 加载(或初始化)壁纸库。state.json 损坏时重命名备份并重建空库。
func Open(statePath, libDir string) (*Library, error) {
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return nil, err
	}
	rand.Seed(time.Now().UnixNano()) // shuffle 模式换图随机(1.19 默认固定种子)
	l := &Library{state: &State{Version: 1, Settings: defaultSettings()}, path: statePath, dir: libDir}
	raw, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(raw, l.state); err != nil || l.state.Version == 0 {
			log.Printf("library: state.json 损坏(%v),备份后重建", err)
			_ = os.Rename(statePath, statePath+".corrupt")
			l.state = &State{Version: 1, Settings: defaultSettings()}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if l.state.Version == 0 {
		l.state.Version = 1
	}
	if l.state.Items == nil {
		l.state.Items = []Item{}
	}
	l.normalize()
	if err := l.persistLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// normalize 清理指向已丢失文件的条目与非法设置值。
func (l *Library) normalize() {
	kept := l.state.Items[:0]
	for _, it := range l.state.Items {
		if it.Slot != "day" && it.Slot != "night" {
			it.Slot = "any"
		}
		if !idRe.MatchString(it.ID) {
			continue
		}
		if _, err := os.Stat(l.itemSrc(it)); err != nil {
			log.Printf("library: 条目 %s(%s) 文件缺失,清理", it.ID, it.Name)
			_ = os.RemoveAll(l.itemDir(it.ID))
			continue
		}
		kept = append(kept, it)
	}
	l.state.Items = kept
	s := &l.state.Settings
	switch s.Mode {
	case "single", "rotation", "timeofday":
	default:
		s.Mode = "rotation"
	}
	if s.IntervalMin < 1 {
		s.IntervalMin = 1
	}
	if s.Order != "seq" {
		s.Order = "shuffle"
	}
	if !isHHMM(s.DayStart) {
		s.DayStart = "07:00"
	}
	if !isHHMM(s.NightStart) {
		s.NightStart = "19:00"
	}
}

func isHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return h < 24 && m < 60
}

// persistLocked 原子落盘(调用方须持锁)。
func (l *Library) persistLocked() error {
	raw, err := json.MarshalIndent(l.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// —— 路径 ——

func (l *Library) itemDir(id string) string { return filepath.Join(l.dir, id) }
func (l *Library) itemSrc(it Item) string   { return filepath.Join(l.itemDir(it.ID), "src."+it.Ext) }

// SrcPath 返回条目源文件绝对路径(校验 id,防路径穿越)。
func (l *Library) SrcPath(id string) (Item, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.srcPathLocked(id)
}

func (l *Library) srcPathLocked(id string) (Item, string, error) {
	if !idRe.MatchString(id) {
		return Item{}, "", ErrNotFound
	}
	for _, it := range l.state.Items {
		if it.ID == id {
			return it, l.itemSrc(it), nil
		}
	}
	return Item{}, "", ErrNotFound
}

// ThumbPath 返回条目缩略图路径(不存在时退回源图)。
func (l *Library) ThumbPath(id string) (string, error) {
	l.mu.Lock()
	it, _, err := l.srcPathLocked(id)
	l.mu.Unlock()
	if err != nil {
		return "", err
	}
	p := filepath.Join(l.itemDir(id), "thumb.jpg")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return l.itemSrc(it), nil
}

// —— 查询 ——

// Snapshot 返回状态副本与当前播放推导结果(公共只读视图)。
func (l *Library) Snapshot(now time.Time) (items []Item, s Settings, cur *Item) {
	l.mu.Lock()
	defer l.mu.Unlock()
	items = append([]Item(nil), l.state.Items...)
	s = l.state.Settings
	cur = l.currentLocked(now)
	return items, s, cur
}

// Current 返回当前应播放的壁纸(按模式/时钟/游标推导,必要时推进游标并落盘)。
func (l *Library) Current(now time.Time) *Item {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentLocked(now)
}

// currentLocked 推导当前条目(调用方须持锁;有状态推进时持久化)。
func (l *Library) currentLocked(now time.Time) *Item {
	st := l.state
	find := func(id string) *Item {
		for i := range st.Items {
			if st.Items[i].ID == id && st.Items[i].Active {
				return &st.Items[i]
			}
		}
		return nil
	}

	actives := func() []*Item {
		var out []*Item
		for i := range st.Items {
			if st.Items[i].Active {
				out = append(out, &st.Items[i])
			}
		}
		// 播单内顺序稳定:按创建时间排序,shuffle 的随机性只在"换下一张"时引入
		sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.Before(out[b].CreatedAt) })
		return out
	}

	pool := actives()
	if len(pool) == 0 {
		st.Rotation.CurrentID = ""
		return nil
	}

	// timeofday:先按本地时钟收窄池子(day/night 槽位,"any" 两边都参与)
	if st.Settings.Mode == "timeofday" {
		slot := "day"
		if inNight(now, st.Settings) {
			slot = "night"
		}
		var slotPool []*Item
		for _, it := range pool {
			if it.Slot == slot || it.Slot == "any" {
				slotPool = append(slotPool, it)
			}
		}
		if len(slotPool) == 0 { // 该时段没有专属壁纸:退回全池
			slotPool = pool
		}
		pool = slotPool
	}

	cur := find(st.Rotation.CurrentID)
	needSwitch := cur == nil
	if !needSwitch && st.Settings.Mode != "single" {
		interval := time.Duration(st.Settings.IntervalMin) * time.Minute
		if now.Sub(st.Rotation.LastSwitch) >= interval {
			needSwitch = true
		}
		// 条目当前槽位不再匹配时段 → 也换(timeofday 下 day→night 翻转)
		if !needSwitch && st.Settings.Mode == "timeofday" && cur.Slot != "any" {
			want := "day"
			if inNight(now, st.Settings) {
				want = "night"
			}
			if cur.Slot != want {
				needSwitch = true
			}
		}
	}
	if !needSwitch {
		return cur
	}

	next := pickNext(pool, cur, st.Settings.Order)
	st.Rotation.CurrentID = next.ID
	st.Rotation.LastSwitch = now
	if err := l.persistLocked(); err != nil {
		log.Printf("library: 轮换游标落盘失败: %v", err)
	}
	return next
}

// pickNext 从池中选"下一张":seq=顺序轮转,shuffle=随机(避免原地重复)。
func pickNext(pool []*Item, cur *Item, order string) *Item {
	if cur == nil {
		if order == "seq" {
			return pool[0]
		}
		return pool[rand.Intn(len(pool))]
	}
	for i, it := range pool {
		if it.ID == cur.ID {
			if order == "seq" {
				return pool[(i+1)%len(pool)]
			}
			if len(pool) == 1 {
				return it
			}
			for {
				n := pool[rand.Intn(len(pool))]
				if n.ID != cur.ID {
					return n
				}
			}
		}
	}
	return pool[0]
}

func inNight(now time.Time, s Settings) bool {
	day, night := parseHHMM(s.DayStart), parseHHMM(s.NightStart)
	m := now.Hour()*60 + now.Minute()
	if day < night { // 常规:07:00-19:00 日间
		return m < day || m >= night
	}
	return m >= night && m < day // 跨午夜配置(如 day=20:00 night=06:00)
}

func parseHHMM(s string) int {
	return int(s[0]-'0')*600 + int(s[1]-'0')*60 + int(s[3]-'0')*10 + int(s[4]-'0')
}

// —— 变更 ——

// Add 登记新条目(源文件已就位后调用)。
func (l *Library) Add(it Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !idRe.MatchString(it.ID) {
		return fmt.Errorf("非法条目 id: %q", it.ID)
	}
	for _, x := range l.state.Items {
		if x.ID == it.ID {
			return fmt.Errorf("条目已存在: %s", it.ID)
		}
	}
	if it.Slot == "" {
		it.Slot = "any"
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now()
	}
	// 首条壁纸自动激活并设为当前,开箱即播
	if len(l.state.Items) == 0 {
		it.Active = true
		l.state.Rotation.CurrentID = it.ID
		l.state.Rotation.LastSwitch = time.Now()
	}
	l.state.Items = append(l.state.Items, it)
	return l.persistLocked()
}

// Update 对单条目应用部分更新(仅白名单字段)。
func (l *Library) Update(id string, name *string, slot *string, active *bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var it *Item
	for i := range l.state.Items {
		if l.state.Items[i].ID == id {
			it = &l.state.Items[i]
			break
		}
	}
	if it == nil {
		return ErrNotFound
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" || len([]rune(n)) > 60 {
			return fmt.Errorf("名称需为 1-60 个字符")
		}
		it.Name = n
	}
	if slot != nil {
		switch *slot {
		case "any", "day", "night":
			it.Slot = *slot
		default:
			return fmt.Errorf("槽位须为 any/day/night")
		}
	}
	if active != nil {
		it.Active = *active
	}
	return l.persistLocked()
}

// Activate 立即播放指定条目(单曲接管,游标重置)。
func (l *Library) Activate(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, it := range l.state.Items {
		if it.ID == id {
			if !it.Active {
				it.Active = true
			}
			l.state.Rotation.CurrentID = id
			l.state.Rotation.LastSwitch = time.Now()
			return l.persistLocked()
		}
	}
	return ErrNotFound
}

// Delete 删除条目并清理文件目录。
func (l *Library) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx := -1
	for i := range l.state.Items {
		if l.state.Items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	l.state.Items = append(l.state.Items[:idx], l.state.Items[idx+1:]...)
	if l.state.Rotation.CurrentID == id {
		l.state.Rotation.CurrentID = ""
	}
	if err := l.persistLocked(); err != nil {
		return err
	}
	return os.RemoveAll(l.itemDir(id))
}

// SetSettings 整体替换播单设置(值域校验后热生效)。
func (l *Library) SetSettings(s Settings) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch s.Mode {
	case "single", "rotation", "timeofday":
	default:
		return fmt.Errorf("模式须为 single/rotation/timeofday")
	}
	if s.IntervalMin < 1 || s.IntervalMin > 1440 {
		return fmt.Errorf("轮换间隔须在 1-1440 分钟")
	}
	if s.Order != "seq" && s.Order != "shuffle" {
		return fmt.Errorf("顺序须为 seq/shuffle")
	}
	if !isHHMM(s.DayStart) || !isHHMM(s.NightStart) {
		return fmt.Errorf("时段须为 HH:MM 格式")
	}
	l.state.Settings = s
	// 模式切换后立即按新模式推导一次,避免旧游标挂在新池外
	l.currentLocked(time.Now())
	return l.persistLocked()
}

// Stats 面板概览数字。
type Stats struct {
	Total  int   `json:"total"`
	Active int   `json:"active"`
	Videos int   `json:"videos"`
	Images int   `json:"images"`
	Bytes  int64 `json:"bytes"`
}

// SnapshotStats 返回库概览。
func (l *Library) SnapshotStats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	var st Stats
	for _, it := range l.state.Items {
		st.Total++
		if it.Active {
			st.Active++
		}
		if it.Kind == "video" {
			st.Videos++
		} else {
			st.Images++
		}
		st.Bytes += it.Bytes
	}
	return st
}
