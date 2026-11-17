/* WallLoop 管理面板逻辑(原生 JS,零依赖;全相对路径,兼容 /wallloop/ iframe 内嵌) */
"use strict";

// BASE:/ 或 /wallloop/(TOS nginx 前缀剥离前),API 返回的相对媒体地址也要拼上
const BASE = new URL(".", document.baseURI).pathname;
const u = (p) => BASE + p;
const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];

let state = null;         // /api/v1/state 全量
let genKind = "mandelbrot";
let renameTarget = null;
let jobsTimer = null;

function fmtBytes(n) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + " GB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  return (n / 1e3).toFixed(0) + " KB";
}
function fmtDur(s) {
  if (!s) return "";
  const m = Math.floor(s / 60), r = Math.round(s % 60);
  return m ? `${m}分${r.toString().padStart(2, "0")}秒` : `${r} 秒`;
}
function toast(msg, cls = "") {
  const t = document.createElement("div");
  t.className = "toast " + cls;
  t.textContent = msg;
  $("#toasts").appendChild(t);
  setTimeout(() => t.remove(), 3600);
}
async function api(path, opts = {}) {
  const res = await fetch(u(path), Object.assign({ headers: {} }, opts));
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}

/* —— 加载与渲染 —— */

async function refresh() {
  try {
    state = await api("api/v1/state");
  } catch (e) {
    toast("加载失败:" + e.message, "err");
    return;
  }
  renderChips();
  renderGrid();
  renderSettings();
  renderPresets();
  renderJobs(state.jobs || []);
  renderNow();
  renderApiUrls();
}

function renderChips() {
  const s = state.stats, set = state.settings;
  const modeName = { rotation: "轮换", timeofday: "日/夜时段", single: "单曲" }[set.mode] || set.mode;
  $("#chips").innerHTML =
    `<span class="chip">壁纸 <b>${s.total}</b>(视频 ${s.videos} · 图片 ${s.images})</span>` +
    `<span class="chip">播单 <b>${s.active}</b></span>` +
    `<span class="chip">模式 <b>${modeName}</b></span>` +
    `<span class="chip">占用 <b>${fmtBytes(s.bytes)}</b></span>`;
}

function renderGrid() {
  const grid = $("#grid"), empty = $("#empty");
  const items = state.items || [];
  $("#lib-count").textContent = items.length ? `${items.length} 项 · 点击缩略图预览` : "";
  grid.innerHTML = "";
  empty.hidden = items.length > 0;
  const curId = state.current && state.current.id;
  for (const it of items) {
    const card = document.createElement("div");
    card.className = "wall" + (it.id === curId ? " current" : "");
    card.innerHTML = `
      <div class="thumbbox" title="点击预览">
        <img loading="lazy" src="${u(it.thumb)}" onerror="this.style.opacity=.15">
        <span class="kind">${it.kind === "video" ? "视频" : "图片"}${it.generated ? " · 生成" : ""}</span>
        ${it.kind === "video" && it.durationSec ? `<span class="dur">${fmtDur(it.durationSec)}</span>` : ""}
      </div>
      <div class="meta">
        <div class="name" title="${esc(it.name)}">${esc(it.name)}</div>
        <div class="sub"><span>${it.width ? `${it.width}×${it.height}` : ""}</span><span>${fmtBytes(it.bytes)}</span></div>
        <div class="ops">
          <span class="slotpick" title="时段槽位(timeofday 模式)">
            <button data-a="slot" data-v="day" class="${it.slot === "day" ? "on" : ""}">日</button>
            <button data-a="slot" data-v="any" class="${it.slot === "any" ? "on" : ""}">任</button>
            <button data-a="slot" data-v="night" class="${it.slot === "night" ? "on" : ""}">夜</button>
          </span>
          <span class="grow"></span>
          <button class="btn small ${it.active ? "primary" : ""}" data-a="active" title="加入/移出播单">${it.active ? "播单中" : "已停用"}</button>
          <button class="btn small" data-a="activate" title="立即播放">▶</button>
          <button class="btn small ghost" data-a="rename" title="重命名">✎</button>
          <button class="btn small ghost danger" data-a="del" title="删除">✕</button>
        </div>
      </div>`;
    card.querySelector(".thumbbox").onclick = () => preview(it);
    card.querySelectorAll("[data-a]").forEach((btn) => {
      btn.onclick = (ev) => { ev.stopPropagation(); itemAction(it, btn.dataset.a, btn.dataset.v); };
    });
    grid.appendChild(card);
  }
}

function esc(s) { return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }

function preview(it) {
  // 复用播放器页预览单条(带 id 参数 = 忽略播单,只循环这一条)
  window.open(u("play.html") + `?id=${it.id}`, "_blank");
}

function renderNow() {
  $("#now-name").textContent = state.current ? state.current.name : "—(播单为空)";
}

/* —— 条目操作 —— */

async function itemAction(it, act, val) {
  try {
    if (act === "del") {
      if (!confirm(`删除「${it.name}」?文件会从 NAS 移除,不可恢复。`)) return;
      await api(`api/v1/items/${it.id}`, { method: "DELETE" });
      toast("已删除:" + it.name);
    } else if (act === "rename") {
      renameTarget = it.id;
      $("#in-rename").value = it.name;
      $("#rename-mask").classList.add("show");
      $("#in-rename").focus();
      return;
    } else if (act === "slot") {
      await api(`api/v1/items/${it.id}/slot`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ slot: val }) });
    } else if (act === "active") {
      await api(`api/v1/items/${it.id}/active`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ active: !it.active }) });
    } else if (act === "activate") {
      await api(`api/v1/items/${it.id}/activate`, { method: "POST" });
      toast("已设为正在播放:" + it.name, "ok");
    }
    refresh();
  } catch (e) {
    toast("操作失败:" + e.message, "err");
  }
}

$("#btn-rename-cancel").onclick = () => $("#rename-mask").classList.remove("show");
$("#btn-rename-ok").onclick = async () => {
  const name = $("#in-rename").value.trim();
  if (!name) return;
  try {
    await api(`api/v1/items/${renameTarget}/rename`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
    $("#rename-mask").classList.remove("show");
    refresh();
  } catch (e) { toast("重命名失败:" + e.message, "err"); }
};
$("#in-rename").addEventListener("keydown", (e) => { if (e.key === "Enter") $("#btn-rename-ok").click(); });

/* —— 上传 —— */

const dz = $("#dropzone"), fi = $("#file-input");
$("#btn-pick").onclick = () => fi.click();
dz.ondragover = (e) => { e.preventDefault(); dz.classList.add("over"); };
dz.ondragleave = () => dz.classList.remove("over");
dz.ondrop = (e) => { e.preventDefault(); dz.classList.remove("over"); uploadFiles(e.dataTransfer.files); };
fi.onchange = () => { uploadFiles(fi.files); fi.value = ""; };

function uploadFiles(files) {
  for (const f of files) uploadOne(f);
}

function uploadOne(file) {
  const row = document.createElement("div");
  row.className = "uprow";
  row.innerHTML = `<span style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(file.name)}</span>
    <span class="bar"><i></i></span><span class="pct">0%</span>`;
  $("#uploads").prepend(row);
  const bar = row.querySelector("i"), pct = row.querySelector(".pct");
  const xhr = new XMLHttpRequest();
  xhr.open("POST", u("api/v1/upload"));
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) {
      const p = Math.round((e.loaded / e.total) * 100);
      bar.style.width = p + "%";
      pct.textContent = p + "%";
    }
  };
  xhr.onload = () => {
    if (xhr.status === 200) {
      bar.style.width = "100%"; pct.textContent = "完成";
      toast("已入库:" + file.name, "ok");
      setTimeout(() => row.remove(), 2500);
    } else {
      pct.textContent = "失败";
      row.style.color = "var(--danger)";
      let msg = "HTTP " + xhr.status;
      try { msg = JSON.parse(xhr.responseText).error || msg; } catch (_) {}
      toast(`上传失败(${file.name}):${msg}`, "err");
    }
    refresh();
  };
  xhr.onerror = () => { pct.textContent = "失败"; toast("上传中断(网络错误)", "err"); };
  const fd = new FormData();
  fd.append("file", file);
  fd.append("name", file.name.replace(/\.[^.]+$/, ""));
  xhr.send(fd);
}

/* —— 播单设置 —— */

const MODE_HINT = {
  rotation: "播单内壁纸按间隔轮换,随机或按加入顺序切换。",
  timeofday: "按本地时钟在日间/夜间壁纸间换挡:7:00-19:00 用「日」槽,其余时间用「夜」槽,「任」槽两边参与。",
  single: "只循环播放当前这一条,不自动切换。",
};

function segVal(id) { return $(`#${id} button.on`).dataset.v; }
function segSet(id, v) { $$(`#${id} button`).forEach((b) => b.classList.toggle("on", b.dataset.v === v)); }

function renderSettings() {
  const s = state.settings;
  segSet("seg-mode", s.mode);
  segSet("seg-order", s.order);
  $("#in-interval").value = s.intervalMin;
  $("#in-day").value = s.dayStart;
  $("#in-night").value = s.nightStart;
  applyModeVisibility();
  $("#set-mode-label").textContent = MODE_HINT[s.mode].slice(0, 12) + "…";
}

function applyModeVisibility() {
  const m = segVal("seg-mode");
  $("#f-interval").style.display = m === "single" ? "none" : "";
  $("#f-order").style.display = m === "single" ? "none" : "";
  $("#f-times").hidden = m !== "timeofday";
  $("#modehint").textContent = MODE_HINT[m];
}

$$("#seg-mode button").forEach((b) => b.onclick = () => { segSet("seg-mode", b.dataset.v); applyModeVisibility(); });
$$("#seg-order button").forEach((b) => b.onclick = () => segSet("seg-order", b.dataset.v));

$("#btn-save-set").onclick = async () => {
  const body = {
    mode: segVal("seg-mode"),
    intervalMin: Math.max(1, Math.min(1440, +$("#in-interval").value || 30)),
    order: segVal("seg-order"),
    dayStart: $("#in-day").value || "07:00",
    nightStart: $("#in-night").value || "19:00",
  };
  try {
    await api("api/v1/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    toast("播单设置已保存", "ok");
    refresh();
  } catch (e) { toast("保存失败:" + e.message, "err"); }
};

/* —— 演示壁纸生成 —— */

function renderPresets() {
  const box = $("#presets");
  box.innerHTML = "";
  for (const p of state.presets || []) {
    const div = document.createElement("div");
    div.className = "preset" + (p.kind === genKind ? " on" : "");
    div.innerHTML = `<input type="radio" name="gen" ${p.kind === genKind ? "checked" : ""}>
      <div><div class="pl">${p.label}</div><div class="pd">${p.detail}</div></div>`;
    div.onclick = () => { genKind = p.kind; renderPresets(); };
    box.appendChild(div);
  }
  $("#f-kensrc").hidden = genKind !== "kenburns";
  if (genKind === "kenburns") {
    const sel = $("#in-kensrc");
    const imgs = (state.items || []).filter((x) => x.kind === "image");
    sel.innerHTML = imgs.length
      ? imgs.map((x) => `<option value="${x.id}">${esc(x.name)}</option>`).join("")
      : `<option value="">(没有图片壁纸 — 请先上传一张)</option>`;
  }
}

$("#in-secs").oninput = () => { $("#secs-val").textContent = $("#in-secs").value + " 秒"; };

$("#btn-gen").onclick = async () => {
  const body = { kind: genKind, seconds: +$("#in-secs").value };
  if (genKind === "kenburns") {
    body.srcId = $("#in-kensrc").value;
    if (!body.srcId) return toast("请先上传一张图片壁纸", "err");
  }
  try {
    const r = await api("api/v1/generate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    toast("生成任务已提交(后台编码中)", "ok");
    pollJobs();
  } catch (e) { toast("提交失败:" + e.message, "err"); }
};

function pollJobs() {
  if (jobsTimer) return;
  jobsTimer = setInterval(async () => {
    try {
      const r = await api("api/v1/jobs");
      renderJobs(r.jobs || []);
      if (!(r.jobs || []).some((j) => j.status === "running")) {
        clearInterval(jobsTimer); jobsTimer = null;
        refresh();
      }
    } catch (_) { /* 轮询失败下轮再试 */ }
  }, 2500);
}

function renderJobs(jobs) {
  const box = $("#jobs");
  box.innerHTML = "";
  const running = jobs.some((j) => j.status === "running");
  if (running && !jobsTimer) pollJobs();
  for (const j of jobs.slice(0, 6)) {
    const div = document.createElement("div");
    div.className = "job";
    const st = j.status === "running" ? `<span class="spin"></span><span class="st run">编码中</span>`
      : j.status === "done" ? `<span class="st done">✓ 完成</span>` : `<span class="st fail">✕ 失败</span>`;
    const label = (state.presets || []).find((p) => p.kind === j.kind)?.label || j.kind;
    div.innerHTML = `${st}<span>${esc(label)}</span>${j.detail ? `<span style="color:var(--ink-faint);overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(j.detail)}">${esc(j.detail)}</span>` : ""}`;
    box.appendChild(div);
  }
}

/* —— 客户端接入 —— */

function renderApiUrls() {
  // 公开面(电视/手机直连):当前主机名 + 服务端公开端口(管理面经 nginx 时主机名不变)
  const pub = `http://${location.hostname}:${state.publicPort || "18240"}`;
  $("#url-play").textContent = pub + "/play";
  $("#url-now").textContent = pub + "/api/v1/now";
  $("#url-list").textContent = pub + "/api/v1/playlist";
}
$$("[data-copy]").forEach((b) => b.onclick = async () => {
  try { await navigator.clipboard.writeText($("#" + b.dataset.copy).textContent); toast("已复制", "ok"); }
  catch (_) { toast("复制失败,请手动选择", "err"); }
});

/* —— 入口 —— */

$("#btn-refresh").onclick = refresh;
$("#btn-play").href = u("play.html");
refresh();
