#!/usr/bin/env bash
# WallLoop 冒烟测试:对运行中的实例过一遍核心 API(上传→播单→轮换→生成→删除)。
# 用法:BASE=http://127.0.0.1:18241 PUB=http://127.0.0.1:18240 ./smoke.sh
# 依赖:curl、ffmpeg(生成测试素材与演示壁纸)
set -euo pipefail
# Windows/Git Bash 下 python 默认按本地码页解码 stdin,会搅乱 UTF-8 断言;Linux 无感
export PYTHONUTF8=1 PYTHONIOENCODING=utf-8

BASE="${BASE:-http://127.0.0.1:18241}"   # 管理面
PUB="${PUB:-http://127.0.0.1:18240}"     # 公开面
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FF="${FFMPEG:-ffmpeg}"

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "== $*"; }

note "healthz(双面)"
curl -fsS "${BASE}/healthz" | grep -q ok || fail "admin healthz"
curl -fsS "${PUB}/healthz" | grep -q ok || fail "public healthz"

note "生成 6 秒测试视频(testsrc2)并上传"
"${FF}" -hide_banner -loglevel error -y -f lavfi -i testsrc2=s=640x360:r=15 \
  -t 6 -c:v libx264 -preset ultrafast -pix_fmt yuv420p "${TMP}/demo.mp4"
ID="$(curl -fsS -F "file=@${TMP}/demo.mp4" -F 'name=smoke-item' "${BASE}/api/v1/upload" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
[ -n "${ID}" ] || fail "upload 没拿到 id"

note "state 里能看到条目"
curl -fsS "${BASE}/api/v1/state" | python3 -c "
import json,sys
st = json.load(sys.stdin)
assert any(i['id']=='${ID}' for i in st['items']), '条目缺失'
assert st['stats']['total'] >= 1
print('   items:', len(st['items']))"

note "UTF-8 改名(--data-binary @file 绕开 Windows curl argv 码页)+ 显式激活"
printf '{"name":"冒烟改名"}' > "${TMP}/rename.json"
curl -fsS -X POST -H 'Content-Type: application/json' --data-binary @"${TMP}/rename.json" \
  "${BASE}/api/v1/items/${ID}/rename" >/dev/null
curl -fsS -X POST "${BASE}/api/v1/items/${ID}/activate" >/dev/null

note "now 接口返回刚激活的条目(公开面)"
curl -fsS "${PUB}/api/v1/now" | python3 -c "
import json,sys
r = json.load(sys.stdin)
assert r['item'] and r['item']['id']=='${ID}', r
assert r['item']['name']=='冒烟改名', r['item']['name']
print('   now:', r['item']['name'], '| mode:', r['mode'])"

note "媒体可拉且支持 Range(公开面)"
curl -fsS -o /dev/null -w '   full=%{http_code} ' "${PUB}/media/${ID}/src.mp4"
curl -fsS -o /dev/null -H 'Range: bytes=0-99' -w 'range=%{http_code}(206 为通过)\n' \
  "${PUB}/media/${ID}/src.mp4" | grep -E '206|200' || fail "Range 请求异常"

note "公开面隔离:管理 API 一律不可达"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "${PUB}/api/v1/upload")"
[ "${CODE}" = "404" ] || fail "公开面上传应 404,得到 ${CODE}"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "${PUB}/api/v1/items/${ID}")"
[ "${CODE}" = "404" ] || fail "公开面删除应 404,得到 ${CODE}"
CODE="$(curl -s -o /dev/null -w '%{http_code}' "${PUB}/api/v1/state")"
[ "${CODE}" = "404" ] || fail "公开面 state 应 404,得到 ${CODE}"
CODE="$(curl -s -o /dev/null -w '%{http_code}' "${PUB}/play")"
[ "${CODE}" = "200" ] || fail "公开面播放器应 200,得到 ${CODE}"

note "槽位 + 停用/启用"
curl -fsS -X POST -H 'Content-Type: application/json' -d '{"slot":"night"}' "${BASE}/api/v1/items/${ID}/slot" >/dev/null
curl -fsS -X POST -H 'Content-Type: application/json' -d '{"active":false}' "${BASE}/api/v1/items/${ID}/active" >/dev/null
curl -fsS "${BASE}/api/v1/state" | python3 -c "
import json,sys
st = json.load(sys.stdin)
it = [i for i in st['items'] if i['id']=='${ID}'][0]
assert it['name']=='冒烟改名' and it['slot']=='night' and it['active'] is False, it
assert st['current'] is None or st['current'].get('id')!='${ID}', '停用后不应在播'"
curl -fsS -X POST -H 'Content-Type: application/json' -d '{"active":true}' "${BASE}/api/v1/items/${ID}/active" >/dev/null

note "播单设置切换(时间段校验拒绝非法值)"
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"mode":"timeofday","intervalMin":30,"order":"shuffle","dayStart":"07:00","nightStart":"19:00"}' \
  "${BASE}/api/v1/settings" >/dev/null
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"mode":"bogus","intervalMin":30,"order":"seq","dayStart":"07:00","nightStart":"19:00"}' \
  "${BASE}/api/v1/settings")"
[ "${CODE}" = "400" ] || fail "非法设置应 400,得到 ${CODE}"

note "演示壁纸生成(mandelbrot 5 秒,同步等完成)"
JOB="$(curl -fsS -X POST -H 'Content-Type: application/json' -d '{"kind":"mandelbrot","seconds":5}' "${BASE}/api/v1/generate" | python3 -c 'import json,sys; print(json.load(sys.stdin)["jobId"])')"
for i in $(seq 1 60); do
  DONE="$(curl -fsS "${BASE}/api/v1/jobs" | python3 -c "
import json,sys
j = [x for x in json.load(sys.stdin)['jobs'] if x['id']=='${JOB}'][0]
print(j['status'])")"
  [ "${DONE}" != "running" ] && break
  sleep 2
done
[ "${DONE}" = "done" ] || fail "生成任务状态 ${DONE}"
curl -fsS "${BASE}/api/v1/state" | python3 -c "
import json,sys
st = json.load(sys.stdin)
gen = [i for i in st['items'] if i.get('generated')]
assert gen and gen[0]['durationSec'] >= 4.5, st
print('   生成条目:', gen[0]['name'], round(gen[0]['durationSec'],1), '秒')"

note "播放器/面板页面可访问"
curl -fsS -o /dev/null "${BASE}/play"
curl -fsS -o /dev/null "${BASE}/"
curl -fsS -o /dev/null "${BASE}/logo.svg"

note "删除测试条目"
curl -fsS -X DELETE "${BASE}/api/v1/items/${ID}" >/dev/null
curl -fsS "${BASE}/api/v1/state" | python3 -c "
import json,sys
st = json.load(sys.stdin)
assert not any(i['id']=='${ID}' for i in st['items'])"

echo "ALL SMOKE TESTS PASSED"
