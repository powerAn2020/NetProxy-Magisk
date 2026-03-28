#!/system/bin/sh
# NetProxy 负载均衡管理脚本
# 薄壳脚本：所有核心逻辑委托给 proxylink balancer 子命令

set -u

readonly MODDIR="$(cd "$(dirname "$0")/../.." && pwd)"
readonly OUTBOUNDS_DIR="$MODDIR/config/xray/outbounds"
readonly PROXYLINK="$MODDIR/bin/proxylink"

show_usage() {
  "$PROXYLINK" balancer help
  exit 0
}

case "${1:-}" in
  create)
    "$PROXYLINK" balancer create \
      -name "${2:-}" -strategy "${3:-}" -sources "${4:-}" \
      -dir "$OUTBOUNDS_DIR"
    ;;
  update)
    "$PROXYLINK" balancer update \
      -name "${2:-}" -strategy "${3:-}" -sources "${4:-}" \
      -dir "$OUTBOUNDS_DIR"
    ;;
  delete)
    "$PROXYLINK" balancer delete -name "${2:-}" -dir "$OUTBOUNDS_DIR"
    ;;
  list)
    "$PROXYLINK" balancer list -dir "$OUTBOUNDS_DIR"
    ;;
  generate)
    "$PROXYLINK" balancer generate -name "${2:-}" -dir "$OUTBOUNDS_DIR"
    ;;
  regenerate-all)
    "$PROXYLINK" balancer regenerate-all -dir "$OUTBOUNDS_DIR"
    ;;
  -h | --help | help | "")
    show_usage
    ;;
  *)
    echo "错误: 未知命令 '$1'"
    show_usage
    ;;
esac
