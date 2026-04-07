#!/usr/bin/env bash
set -euo pipefail

# 一键构建：后端二进制（server/agent 如存在）+ 前端静态资源
# 产物：
#   - dist/server         (若存在 cmd/server)
#   - dist/agent          (若存在 cmd/agent)
#   - web/dist/           (前端打包产物)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIST_DIR="${DIST_DIR:-${ROOT_DIR}/dist}"
# 交互式：若未指定则询问；非交互（CI）时通过环境变量控制
BUILD_WINDOWS="${BUILD_WINDOWS:-ask}"   # ask|0|1
WIN_TARGETS="${WIN_TARGETS:-windows/amd64 windows/386}"  # 多个用空格分隔，可包含 windows/arm64

echo "[build] root: ${ROOT_DIR}"
mkdir -p "${DIST_DIR}"

have_tty() {
  [[ -t 0 && -t 1 ]]
}

prompt_yes_no() {
  local q="$1"; local def="${2:-n}"
  local prompt="[y/N]"
  if [[ "${def}" == "y" || "${def}" == "Y" ]]; then prompt="[Y/n]"; fi
  while true; do
    read -r -p "${q} ${prompt} " ans || { echo; return 1; }
    ans="${ans:-$def}"
    case "${ans}" in
      y|Y) return 0 ;;
      n|N) return 1 ;;
      *) echo "请输入 y 或 n."; ;;
    esac
  done
}

go_check() {
  if ! command -v go >/dev/null 2>&1; then
    echo "[build] 未检测到 Go，请先安装 Go 并配置 PATH."
    exit 1
  fi
}

node_check() {
  if [[ -f "${ROOT_DIR}/web/package.json" ]] && ! command -v npm >/dev/null 2>&1; then
    echo "[build] 检测到 web/ 子项目，但未安装 Node/npm，请先安装 Node.js."
    exit 1
  fi
}

build_go() {
  local name="$1"
  local pkg="$2"
  if [[ -d "${ROOT_DIR}/${pkg}" ]]; then
    echo "[build] go build ${name} (${pkg})"
    (cd "${ROOT_DIR}" && go build -o "${DIST_DIR}/${name}" "./${pkg}")
    echo "[build] output: ${DIST_DIR}/${name}"
  else
    echo "[build] skip ${name} (missing ${pkg})"
  fi
}

build_go_windows() {
  local name="$1"
  local pkg="$2"
  if [[ ! -d "${ROOT_DIR}/${pkg}" ]]; then
    echo "[build] skip ${name} (missing ${pkg})"
    return 0
  fi
  for target in ${WIN_TARGETS}; do
    local goos="${target%%/*}"
    local goarch="${target##*/}"
    local out="${DIST_DIR}/${name}-${goos}-${goarch}.exe"
    echo "[build] go build ${name} for ${goos}/${goarch} -> ${out}"
    (cd "${ROOT_DIR}" && \
      CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
      go build -o "${out}" "./${pkg}")
  done
}

build_frontend() {
  if [[ -f "${ROOT_DIR}/web/package.json" ]]; then
    echo "[build] frontend deps check (npm ci if lock exists, else npm install)"
    cd "${ROOT_DIR}/web"
    if [[ -f "package-lock.json" ]]; then
      npm ci
    else
      npm install
    fi
    echo "[build] npm run build"
    npm run build
    echo "[build] frontend built at: ${ROOT_DIR}/web/dist"
  else
    echo "[build] skip frontend (web/package.json not found)"
  fi
}

go_check
node_check

# 交互：是否构建 Windows 版本
if [[ "${BUILD_WINDOWS}" == "ask" ]]; then
  if have_tty; then
    if prompt_yes_no "是否额外构建 Windows 可执行文件（server/agent）？" "n"; then
      BUILD_WINDOWS="1"
      echo
      echo "请选择要构建的 Windows 目标（可多选，编号用空格或逗号分隔，回车使用默认）："
      local_presets=("windows/amd64" "windows/386" "windows/arm64")
      for i in "${!local_presets[@]}"; do
        printf "  %d) %s\n" "$((i+1))" "${local_presets[$i]}"
      done
      echo "  0) 自定义（手动输入 GOOS/GOARCH 列表，如：windows/amd64 windows/arm64）"
      echo
      read -r -p "选择编号（默认: 1 2）: " sel || true
      sel="${sel:-1 2}"
      # 解析选择
      IFS=', ' read -r -a arr <<< "${sel}"
      if [[ "${#arr[@]}" -eq 1 && "${arr[0]}" == "0" ]]; then
        read -r -p "自定义 WIN_TARGETS（空格分隔）= " custom || true
        if [[ -n "${custom:-}" ]]; then
          WIN_TARGETS="${custom}"
        fi
      else
        chosen=()
        for x in "${arr[@]}"; do
          case "${x}" in
            1) chosen+=("${local_presets[0]}") ;;
            2) chosen+=("${local_presets[1]}") ;;
            3) chosen+=("${local_presets[2]}") ;;
            *) ;;
          esac
        done
        if [[ "${#chosen[@]}" -gt 0 ]]; then
          WIN_TARGETS="$(printf "%s " "${chosen[@]}")"
        fi
      fi
    else
      BUILD_WINDOWS="0"
    fi
  else
    BUILD_WINDOWS="0"
  fi
fi

echo "[build] Summary"
echo "  - output dir: ${DIST_DIR}"
echo "  - build server: $( [[ -d ${ROOT_DIR}/cmd/server ]] && echo yes || echo no )"
echo "  - build agent:  $( [[ -d ${ROOT_DIR}/cmd/agent ]] && echo yes || echo no )"
echo "  - build web:    $( [[ -f ${ROOT_DIR}/web/package.json ]] && echo yes || echo no )"
echo "  - windows exe:  ${BUILD_WINDOWS} ${BUILD_WINDOWS:+(${WIN_TARGETS})}"
echo

build_go "server" "cmd/server"
build_go "agent" "cmd/agent"
if [[ "${BUILD_WINDOWS}" == "1" ]]; then
  build_go_windows "server" "cmd/server"
  build_go_windows "agent" "cmd/agent"
fi
build_frontend

echo "[build] done."

