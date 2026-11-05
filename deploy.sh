#!/usr/bin/env bash

# 打包编译并重新部署 elight-token-gateway（版本号自动 +1，如 v21 -> v22）。
# 实际构建、改 tag、重启逻辑见 deploy/rebuild-restart.sh。
#
# 用法：
#   ./deploy.sh            构建并重启
#   ./deploy.sh --push     构建后推送镜像到 Harbor 再重启
#   ./deploy.sh --help     帮助
set -Eeuo pipefail
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deploy/rebuild-restart.sh" "$@"
