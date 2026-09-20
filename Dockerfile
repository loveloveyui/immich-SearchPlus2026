# ==========================================
# 阶段 1：构建前端静态资源 (Node.js 环境)
# ==========================================
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend

COPY frontend/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm install

# 源码层缓存穿透参数
ARG REBUILD_DATE=""

COPY frontend/ ./
# 核心联动：监听 backend 目录变动以自动刷新前端构建时间戳
COPY backend/ ./backend_trigger/

RUN npm run build

# ==========================================
# 阶段 2：编译 Go 嵌入式单二进制 (Golang 环境)
# ==========================================
FROM golang:1.26-alpine AS backend-builder
WORKDIR /app/backend

ENV GOTOOLCHAIN=auto

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend/ ./
COPY --from=frontend-builder /app/backend/dist ./dist
COPY inject.js ./dist/inject.js

# 接收目标系统与架构 (默认 linux / amd64)
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    # 自动识别系统：若为 windows 则补齐 .exe，其余系统保持空
    EXT="" && [ "$TARGETOS" = "windows" ] && EXT=".exe"; \
    BIN_NAME="immich-SearchPlus2026-${TARGETOS}-${TARGETARCH}${EXT}"; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o "/app/${BIN_NAME}" . && \
    # 生成无后缀标准副本，保障阶段 4 容器构建不受影响
    cp "/app/${BIN_NAME}" /app/immich-SearchPlus2026

# ==========================================
# 阶段 3：二进制专用导出层 (专供提取到宿主机)
# ==========================================
FROM scratch AS export-stage
# 自动匹配带架构与系统后缀的文件 (包括 .exe)
COPY --from=backend-builder /app/immich-SearchPlus2026-* /

# ==========================================
# 阶段 4：最终运行镜像 (Compose 默认镜像，保持不变)
# ==========================================
FROM alpine:3.20 AS runner
WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata && \
    adduser -D -u 10001 appuser

ENV TZ=Asia/Shanghai

COPY --from=backend-builder /app/immich-SearchPlus2026 /app/immich-SearchPlus2026

USER appuser
EXPOSE 1880
ENTRYPOINT ["/app/immich-SearchPlus2026"]