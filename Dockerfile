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
# 核心联动：将 backend 目录作为缓存失效感知点
# 一旦 backend/ 下的任何文件（如 main.go）发生变动，本层哈希改变，
# Docker 会自动废弃下一行的构建缓存，强制执行 npm run build 刷新时间戳！
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
# 先并入前端产物，再并入胶水脚本供 go:embed 静态打包
COPY --from=frontend-builder /app/backend/dist ./dist
COPY inject.js ./dist/inject.js

# 自适应多架构构建 (支持 amd64, arm64 等)
ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /app/server .

# ==========================================
# 阶段 3：二进制专用导出层 (专供提取到宿主机，避开软链接报错)
# ==========================================
FROM scratch AS export-stage
COPY --from=backend-builder /app/server /server

# ==========================================
# 阶段 4：最终运行镜像 (保留在最后，默认构建产物)
# ==========================================
FROM alpine:3.20 AS runner
WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata && \
    adduser -D -u 10001 appuser

# 默认运行时区对齐东八区 (容器运行时仍可通过 Compose 的 TZ 覆盖)
ENV TZ=Asia/Shanghai

COPY --from=backend-builder /app/server /app/server

USER appuser
EXPOSE 1880
ENTRYPOINT ["/app/server"]