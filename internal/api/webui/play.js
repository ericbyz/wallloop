/* WallLoop 播放器:全屏循环播放当前壁纸,双缓冲交叉淡入,轮询跟随播单切换 */
"use strict";

const BASE = new URL(".", document.baseURI).pathname;
const u = (p) => BASE + p;
const $ = (s) => document.querySelector(s);

const la = $("#la"), lb = $("#lb"), hud = $("#hud");
let front = la, back = lb;      // 双缓冲:back 预载新壁纸,载好后再交叉淡入
let curID = null;               // 正在展示的条目 id(避免轮询重复触发切换)
let pollTimer = null;

// ?id=<itemId> = 单条预览模式(面板缩略图点击进入)
const previewID = new URLSearchParams(location.search).get("id");

async function fetchItem() {
  if (previewID) {
    // 预览:走 state 缓存出该条目(只读,不推进轮换)
    const st = await (await fetch(u("api/v1/state"))).json();
    return (st.items || []).find((x) => x.id === previewID) || null;
  }
  const r = await (await fetch(u("api/v1/now"), { cache: "no-store" })).json();
  return r.item;
}

function mediaURL(rel) { return u(rel); }

function mount(layer, item) {
  layer.innerHTML = "";
  if (item.kind === "video") {
    const v = document.createElement("video");
    v.src = mediaURL(item.url);
    v.muted = true; v.loop = true; v.playsInline = true; v.autoplay = true;
    v.setAttribute("playsinline", "");
    layer.appendChild(v);
    v.play().catch(() => {}); // 自动播放策略兜底:首帧仍会渲染
  } else {
    const img = document.createElement("img");
    img.src = mediaURL(item.url);
    layer.appendChild(img);
  }
}

async function tick() {
  let item = null;
  try { item = await fetchItem(); } catch (_) { schedule(10); return; }
  if (!item) {
    showIdle(previewID ? "这条壁纸不存在了" : "壁纸库为空");
    schedule(30);
    return;
  }
  $("#idle").classList.add("hidden");
  if (item.id !== curID) {
    curID = item.id;
    // back 预载,就绪后交叉淡入(视频 canplay / 图片 load)
    mount(back, item);
    await new Promise((resolve) => {
      const v = back.querySelector("video"), im = back.querySelector("img");
      const done = () => setTimeout(resolve, 60);
      if (v) {
        if (v.readyState >= 3) done();
        else { v.addEventListener("canplay", done, { once: true }); v.addEventListener("error", done, { once: true }); }
        setTimeout(done, 4000); // 弱网兜底,最多等 4s
      } else if (im) {
        if (im.complete) done();
        else { im.addEventListener("load", done, { once: true }); im.addEventListener("error", done, { once: true }); }
        setTimeout(done, 4000);
      } else done();
    });
    back.classList.add("show");
    front.classList.remove("show");
    [front, back] = [back, front];
    $("#hud-name").textContent = item.name;
  }
  // 跟随服务端提示决定下次检查时间(sleep 期间壁纸照常循环播放)
  schedule(previewID ? 3600 : 30);
}

function schedule(sec) {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(tick, sec * 1000);
}

function showIdle(title) {
  $("#idle").classList.remove("hidden");
  $("#idle-title").textContent = title;
  curID = null;
  la.classList.remove("show"); lb.classList.remove("show");
}

/* —— 交互:HUD 自动隐藏 / 光标隐藏 / 双击全屏 —— */

let hudTimer = null;
function wake() {
  hud.classList.remove("hide");
  document.body.classList.remove("hidecursor");
  clearTimeout(hudTimer);
  hudTimer = setTimeout(() => {
    hud.classList.add("hide");
    document.body.classList.add("hidecursor");
  }, 3500);
}
["mousemove", "touchstart", "keydown"].forEach((ev) =>
  window.addEventListener(ev, wake, { passive: true })
);
window.addEventListener("dblclick", () => {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  else document.documentElement.requestFullscreen().catch(() => {});
});

$("#hud-panel").href = BASE;
$("#idle-link").href = BASE;

tick();
wake();
