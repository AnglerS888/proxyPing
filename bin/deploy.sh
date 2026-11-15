#!/bin/bash
# ============================================================
# 代理 IP 测速台 - 一键部署脚本
#
# 流程：下载最新代码 → 构建镜像 → 替换容器 → 健康检查（失败自动回滚）
#
# 为什么用「代码归档包」而不是 git clone：
#   部署机常常没装 git、也没有仓库凭据。各家的 /archive/<分支>.tar.gz
#   直接给一个压缩包，curl 就能拿，服务器上只要有 docker + curl + tar 即可。
#   默认按 Gitee → GitHub → 自建 Gitea 的顺序试，哪个能下用哪个。
#
# 用法：
#   curl -fsSL http://git.danran0.cc/danran/proxyPing/raw/branch/main/bin/deploy.sh | bash -s deploy
#
#   bash bin/deploy.sh                    # 部署 main 分支（自动挑源）
#   bash bin/deploy.sh logs               # 跟踪日志（另有 restart / stop / status）
#   BRANCH=dev bash bin/deploy.sh         # 部署指定分支
#   SOURCE=github bash bin/deploy.sh      # 只用某个源：gitee / github / gitea
#   PORT=9099 bash bin/deploy.sh          # 换宿主机端口（容器内始终 8899）
#   NO_CACHE=1 bash bin/deploy.sh         # 不用构建缓存（改过 Dockerfile 时用）
#   HEALTH_TIMEOUT=40 bash bin/deploy.sh  # 机器很慢时放宽健康检查等待
# ============================================================

set -euo pipefail

# ---------- 配置（都可用环境变量覆盖） ----------
IMAGE_NAME="${IMAGE_NAME:-proxyip}"
CONTAINER_NAME="${CONTAINER_NAME:-proxyip}"
PORT="${PORT:-8899}"
BRANCH="${BRANCH:-main}"

# 代码来源，按顺序回退。国内机器基本都能拿到 Gitee，海外机器通常走 GitHub 更快，
# 自建 Gitea 留作内网兜底（也是这个项目的原始仓库）
GITEA_BASE="${GITEA_BASE:-http://git.danran0.cc/danran/proxyPing}"
SOURCES=(
    "gitee|https://gitee.com/anglers/proxyPing/repository/archive/${BRANCH}.tar.gz"
    "github|https://github.com/AnglerS888/proxyPing/archive/refs/heads/${BRANCH}.tar.gz"
    "gitea|${GITEA_BASE}/archive/${BRANCH}.tar.gz"
)

# 容器内固定监听 8899（镜像 EXPOSE 也是它）。
# 只映射宿主机侧端口，避免 PORT 和 PROXYIP_PORT 两边不一致导致"容器在跑但访问不通"
CONTAINER_PORT=8899

# 健康检查最多等多少秒：要留够"镜像启动 + 首屏编译资源"的时间
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-20}"

# 上一版镜像标签，用于回滚
PREV_TAG="${IMAGE_NAME}:previous"
LATEST_TAG="${IMAGE_NAME}:latest"

# ---------- 颜色 ----------
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[INFO]${NC}  $(date '+%H:%M:%S')  $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $(date '+%H:%M:%S')  $1"; }
err()  { echo -e "${RED}[ERROR]${NC} $(date '+%H:%M:%S')  $1"; }

TMP_DIR=""
cleanup() {
    # 失败时也别把几十 MB 的源码包留在 /tmp 里。
    # 注意结尾的 return 0：trap 里最后一条命令失败会改写脚本退出码
    # （没下载源码时 TMP_DIR 是空的，[ -n "" ] 返回 1，会让 help/status 也"失败"）
    if [ -n "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
    return 0
}
trap cleanup EXIT

# ---------- 环境检查 ----------
check_env() {
    if ! command -v docker &>/dev/null; then
        err "Docker 未安装，请先安装 Docker"
        exit 1
    fi
    # docker info 能过滤掉"装了但服务没起 / 当前用户没权限"这两种情况，
    # 否则后面构建时会甩一堆看不懂的 permission denied
    if ! docker info &>/dev/null; then
        err "Docker 服务未运行，或当前用户没有 docker 权限（可试 sudo）"
        exit 1
    fi
    for c in curl tar; do
        if ! command -v "$c" &>/dev/null; then
            err "缺少命令: $c"
            exit 1
        fi
    done

    # 端口被非本容器的进程占着时，新容器会起不来；提前说清楚比事后看日志强
    if ! docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
        if command -v ss &>/dev/null && ss -lnt 2>/dev/null | grep -q ":${PORT} "; then
            err "宿主机 ${PORT} 端口已被占用（且不是 $CONTAINER_NAME 容器）"
            warn "换个端口: PORT=9099 bash bin/deploy.sh，或先把占用进程停掉"
            exit 1
        fi
    fi
}

# ---------- 1. 下载源码 ----------
download_source() {
    TMP_DIR=$(mktemp -d)
    local pkg="$TMP_DIR/src.tar.gz"
    local reached="" entry name url

    for entry in "${SOURCES[@]}"; do
        name="${entry%%|*}"
        url="${entry#*|}"

        # 指定了 SOURCE 就只试这一个源
        if [ -n "${SOURCE:-}" ] && [ "$name" != "$SOURCE" ]; then
            continue
        fi

        log "  尝试 $name: $url"
        # --retry：源站偶尔 502；--max-time 兜住"卡住不动"的情况
        if curl -fsSL --retry 2 --retry-delay 2 --connect-timeout 15 --max-time 180 \
                "$url" -o "$pkg"; then
            reached="$name"
            break
        fi
        warn "  $name 拿不到，换下一个源"
        rm -f "$pkg"
    done

    if [ -z "$reached" ]; then
        err "下载失败：可选源都不可用"
        warn "可用 SOURCE=gitee|github|gitea 指定单个源；分支名不对也会 404（当前 BRANCH=$BRANCH）"
        exit 1
    fi
    log "  ✓ 已从 $reached 下载"

    if ! tar -xzf "$pkg" -C "$TMP_DIR"; then
        err "解压失败，下载到的可能不是 tar.gz（源站返回了错误页？）"
        exit 1
    fi

    SRC_DIR=$(find "$TMP_DIR" -mindepth 1 -maxdepth 1 -type d | head -1)
    if [ -z "$SRC_DIR" ]; then
        err "解压后未找到源码目录"
        exit 1
    fi

    # 提前校验两个最容易漏的东西：没有 Dockerfile 就没法构建；
    # 没有 web/ 的话 go:embed 会在编译阶段直接失败（而这个错报在 build 日志深处，很难第一眼看到）
    if [ ! -f "$SRC_DIR/Dockerfile" ]; then
        err "源码里没有 Dockerfile"
        exit 1
    fi
    if [ ! -d "$SRC_DIR/web" ]; then
        err "源码里缺少 web/ 目录（go:embed 依赖它，编译会失败）"
        exit 1
    fi

    # 顺手报一下要部署的版本号，事后能对上"这次上的是哪个版本"
    local ver
    ver=$(grep -oE 'Version = "[^"]+"' "$SRC_DIR/main.go" 2>/dev/null | head -1 | cut -d'"' -f2 || true)
    log "  ✓ 代码就绪 -> $(basename "$SRC_DIR")${ver:+  版本 v$ver}"
}

# ---------- 2. 构建镜像 ----------
build_image() {
    # 构建前先把当前线上镜像打成 previous：新镜像跑不起来时靠它回滚。
    # 注意顺序——必须先 tag 再 build，否则 :latest 已经被新镜像覆盖，previous 就没意义了
    if docker image inspect "$LATEST_TAG" >/dev/null 2>&1; then
        docker tag "$LATEST_TAG" "$PREV_TAG"
        log "  已保留上一版镜像: $PREV_TAG"
        HAS_PREVIOUS=1
    else
        HAS_PREVIOUS=0
    fi

    log "  构建中（首次会比较慢，之后有层缓存）..."
    if [ "${NO_CACHE:-0}" = "1" ]; then
        docker build --no-cache -t "$LATEST_TAG" "$SRC_DIR"
    else
        docker build -t "$LATEST_TAG" "$SRC_DIR"
    fi
    log "  ✓ 镜像构建完成: $LATEST_TAG"
}

# ---------- 3. 替换容器 ----------
run_container() {
    local image="$1"
    docker run -d \
        --name "$CONTAINER_NAME" \
        --restart=unless-stopped \
        -p "${PORT}:${CONTAINER_PORT}" \
        -e TZ=Asia/Shanghai \
        "$image" >/dev/null
}

replace_container() {
    if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
        # rm -f 一步完成 stop + rm，省掉两次等待
        log "  移除旧容器..."
        docker rm -f "$CONTAINER_NAME" >/dev/null
    fi
    log "  启动新容器..."
    run_container "$LATEST_TAG"
}

# ---------- 4. 健康检查 ----------
# 这里刻意不看"容器是不是 running"就收工：
# 崩在启动阶段的容器会被 --restart 反复拉起来，docker ps 里一直是 Up（只是秒退），
# 只有真的发一个 HTTP 请求拿到响应，才算部署成功
health_check() {
    local deadline=$((SECONDS + HEALTH_TIMEOUT))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
            return 1
        fi
        # --noproxy '*'：服务器上常配了 HTTP_PROXY，代理会把本地请求吃掉，
        # 导致"服务明明是好的却探测失败"（这个坑踩过，专门加的参数）
        if curl -fsS --noproxy '*' --max-time 2 "http://127.0.0.1:${PORT}/version" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

rollback() {
    if [ "${HAS_PREVIOUS:-0}" != "1" ]; then
        err "没有可回滚的上一版镜像，请手动排查：docker logs $CONTAINER_NAME"
        return 1
    fi
    warn "尝试回滚到 $PREV_TAG ..."
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
    run_container "$PREV_TAG"
    if health_check; then
        warn "已回滚，服务恢复正常（代码问题请修完再部署）"
    else
        err "回滚后仍不通，请手动排查: docker logs $CONTAINER_NAME"
    fi
    return 1
}

# ---------- 主流程 ----------
cmd_deploy() {
    echo ""
    echo -e "${GREEN}============================================${NC}"
    echo -e "${GREEN}  代理 IP 测速台 - 自动部署${NC}"
    echo -e "${GREEN}============================================${NC}"
    echo -e "  代码分支: ${YELLOW}${BRANCH}${NC}（来源: ${SOURCE:-gitee → github → gitea 依次尝试}）"
    echo -e "  镜像/容器: ${YELLOW}${IMAGE_NAME} / ${CONTAINER_NAME}${NC}"
    echo -e "  访问端口: ${YELLOW}${PORT} -> ${CONTAINER_PORT}${NC}"
    echo ""

    check_env

    log "[1/4] 下载最新代码..."
    download_source

    log "[2/4] 构建 Docker 镜像..."
    build_image

    log "[3/4] 替换容器..."
    replace_container

    log "[4/4] 健康检查（最多等 ${HEALTH_TIMEOUT}s）..."
    if ! health_check; then
        err "新版本没起来，最近日志如下："
        docker logs --tail 40 "$CONTAINER_NAME" 2>&1 || true
        rollback
        exit 1
    fi

    echo ""
    echo -e "${GREEN}============================================${NC}"
    echo -e "${GREEN}  部署成功！${NC}"
    echo -e "${GREEN}============================================${NC}"
    echo -e "  访问地址: ${YELLOW}http://localhost:${PORT}${NC}"
    echo -e "  查看日志: ${YELLOW}docker logs -f ${CONTAINER_NAME}${NC}"
    echo -e "  健康检查: ${YELLOW}curl -s http://localhost:${PORT}/version${NC}"
    echo -e "  回滚镜像: ${YELLOW}${PREV_TAG}${NC}（保留最近一版）"
    echo -e "${GREEN}============================================${NC}"
}

# ============================================================
# 子命令
#
# `curl ... | bash -s deploy` 会把 deploy 当成 $1 传进来，
# 所以这里必须认这个参数；不传等同于 deploy。
# ============================================================

usage() {
    cat <<'EOT'
用法: deploy.sh [命令]

  deploy    下载最新代码 → 构建镜像 → 替换容器 → 健康检查（默认）
  restart   重启容器
  stop      停止并删除容器（镜像保留）
  status    查看容器状态与健康接口
  logs      跟踪容器日志（Ctrl+C 退出）
  help      显示这段帮助

环境变量: PORT BRANCH SOURCE NO_CACHE HEALTH_TIMEOUT IMAGE_NAME CONTAINER_NAME
EOT
}

# require_container 除 deploy 外的命令都要先确认容器存在
require_container() {
    if ! docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER_NAME"; then
        err "容器 $CONTAINER_NAME 不存在，先执行一次 deploy"
        return 1
    fi
}

cmd_restart() {
    check_env
    require_container || exit 1
    log "重启 $CONTAINER_NAME ..."
    docker restart "$CONTAINER_NAME" >/dev/null
    if health_check; then
        log "✓ 已重启，服务正常"
    else
        err "重启后健康检查没通过，最近日志："
        docker logs --tail 30 "$CONTAINER_NAME" 2>&1 || true
        exit 1
    fi
}

cmd_stop() {
    check_env
    if require_container; then
        # rm -f 一步到位；镜像留着，下次 deploy 还能用
        docker rm -f "$CONTAINER_NAME" >/dev/null
        log "✓ 已删除容器 $CONTAINER_NAME（镜像 ${LATEST_TAG} 保留）"
    else
        warn "已经没在跑了"
    fi
}

cmd_status() {
    check_env
    require_container || exit 1
    docker ps -a --filter "name=^${CONTAINER_NAME}$" \
        --format '  状态: {{.Status}}
  镜像: {{.Image}}
  端口: {{.Ports}}'
    printf '  接口: '
    curl -s --noproxy '*' --max-time 3 "http://127.0.0.1:${PORT}/version" || echo "（访问不通）"
    echo
}

cmd_logs() {
    check_env
    require_container || exit 1
    docker logs -f --tail 200 "$CONTAINER_NAME"
}

# ---------- 入口 ----------
case "${1:-deploy}" in
    deploy | update) cmd_deploy ;;
    restart)         cmd_restart ;;
    stop)            cmd_stop ;;
    status)          cmd_status ;;
    logs)            cmd_logs ;;
    help | -h | --help) usage ;;
    *)
        err "未知命令: $1"
        usage
        exit 1
        ;;
esac
