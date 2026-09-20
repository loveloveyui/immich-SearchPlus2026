# ==========================================
# 阶段 1：构建前端静态资源 (Node.js 环境)
# ==========================================
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend

# 安装时区支持，双重保障构建时间戳准确
RUN apk --no-cache add tzdata
ENV TZ=Asia/Shanghai

COPY frontend/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm install

# 源码层缓存穿透参数（日常开发留空，强制刷新时间戳时传入动态值）
ARG REBUILD_DATE=""

COPY frontend/ ./
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

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
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

RUN apk --no-cache add ca-certificates tzdata

# 拷贝阶段 2 生成的单二进制 (产物自带 0755 权限，无需 RUN chmod 避免镜像体积膨胀)
COPY --from=backend-builder /app/server /app/server

EXPOSE 1880
ENTRYPOINT ["/app/server"]