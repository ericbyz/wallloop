// Package library 的演示壁纸生成任务:ffmpeg 在 NAS 本机现场编码,
// 720p25 的 15-120 秒片段在低端 CPU 上也可能要跑几分钟,故异步化 + 面板轮询。
package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Job 一次生成任务的内存态(不落盘;重启即作废,无一致性负担)。
type Job struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"` // running | done | failed
	Detail    string     `json:"detail,omitempty"`
	ItemID    string     `json:"itemId,omitempty"` // 成功后登记的壁纸条目
	CreatedAt time.Time  `json:"createdAt"`
	DoneAt    *time.Time `json:"doneAt,omitempty"`
}

// Jobs 任务表(容量上限,防面板误触刷爆内存)。
type Jobs struct {
	mu    sync.Mutex
	m     map[string]*Job
	order []string
}

// NewJobs 建表。
func NewJobs() *Jobs { return &Jobs{m: map[string]*Job{}} }

const jobsMax = 50

// Snapshot 只读副本(最新在前)。
func (j *Jobs) Snapshot() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.order))
	for i := len(j.order) - 1; i >= 0; i-- {
		out = append(out, *j.m[j.order[i]])
	}
	return out
}

func (j *Jobs) add(kind string) *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.order) >= jobsMax { // 挤掉最老的一条
		old := j.order[0]
		j.order = j.order[1:]
		delete(j.m, old)
	}
	job := &Job{ID: NewID(), Kind: kind, Status: "running", CreatedAt: time.Now()}
	j.m[job.ID] = job
	j.order = append(j.order, job.ID)
	return job
}

func (j *Jobs) finish(id, status, detail, itemID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if job, ok := j.m[id]; ok {
		job.Status = status
		job.Detail = detail
		job.ItemID = itemID
		now := time.Now()
		job.DoneAt = &now
	}
}

// StartGen 启动一次演示壁纸生成:预设/秒数校验 → 起协程跑 ffmpeg → 成功后入库。
// srcPath 仅 kenburns 用(取既有图片壁纸源文件)。
func StartGen(jobs *Jobs, lib *Library, ffmpeg, libDir, preset string, seconds int, srcPath string) (*Job, error) {
	if _, ok := presetIndex(preset); !ok {
		return nil, fmt.Errorf("未知预设: %s", preset)
	}
	if seconds < 5 || seconds > 120 {
		return nil, fmt.Errorf("时长须在 5-120 秒")
	}
	job := jobs.add(preset)

	id := NewID()
	dir := filepath.Join(libDir, id)
	out := filepath.Join(dir, "src.mp4")
	go func() {
		if err := runGen(jobs, lib, ffmpeg, preset, seconds, srcPath, dir, out, id, job.ID); err != nil {
			jobs.finish(job.ID, "failed", err.Error(), "")
			return
		}
		jobs.finish(job.ID, "done", "", id)
	}()
	return job, nil
}

// runGen 执行一次生成的全部步骤,任何一步失败即清理目录并返回错误。
func runGen(jobs *Jobs, lib *Library, ffmpeg, preset string, seconds int, srcPath, dir, out, id, jobID string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fail := func(err error) error {
		os.RemoveAll(dir)
		return err
	}
	// kenburns 之外 srcPath 必须为空,防把上传文件当滤镜输入
	if preset != "kenburns" {
		srcPath = ""
	}
	args, err := GenerateArgs(ffmpeg, preset, seconds, srcPath, out)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if outb, err := cmd.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("%s", trimOut(outb)))
	}
	dur, w, h := Probe(ffmpeg, out)
	if err := MakeThumb(ffmpeg, out, filepath.Join(dir, "thumb.jpg"), "video"); err != nil {
		return fail(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		return fail(err)
	}
	item := Item{ID: id, Name: genName(preset, seconds), Kind: "video", Ext: "mp4",
		Bytes: fi.Size(), DurationSec: dur, Width: w, Height: h, Active: true, Generated: true, CreatedAt: time.Now()}
	if err := lib.Add(item); err != nil {
		return fail(err)
	}
	return nil
}

func presetIndex(kind string) (GenPreset, bool) {
	for _, p := range GenPresets {
		if p.Kind == kind {
			return p, true
		}
	}
	return GenPreset{}, false
}

func genName(preset string, seconds int) string {
	label := preset
	if p, ok := presetIndex(preset); ok {
		label = p.Label
	}
	return fmt.Sprintf("%s(%d 秒·生成)", label, seconds)
}

func trimOut(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = s[len(s)-300:] // ffmpeg 尾部才有错误摘要
	}
	return "ffmpeg: " + s
}
