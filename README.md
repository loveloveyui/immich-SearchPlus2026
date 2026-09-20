# Immich SearchPlus2026 🔍

> 为自建 Immich 相册打造的以图搜图、人脸搜人、人脸相似度 1:1 比对的伴生服务。

_Disclaimer: This is an unofficial, community-driven sidecar extension. It is not affiliated with, endorsed by, or sponsored by the official Immich team._

---

## ✨ 核心特性

- 🖼️ **以图搜图 (CLIP)**：就是用图片搜索图片不需要上传到图库再查找相似啦~欢呼！
- 👤 **人脸多目标匹配 (InsightFace)**：自动识别人脸位置并支持多选，穿透检索相册内同一人物所有照片。
- 👥 **人脸 1:1 独立比对**：双样本余弦相似度极速比对，支持通过专属密钥免相册直通运行（访问 `?app=compare`）。
- ⚡ **原生视口接管**：通过轻量脚本无感嵌入 Immich 原生导航栏，支持移动端返回手势与相册时间线回退重定位。
- 🛡️ **纯只读旁路安全**：直连 pgvector 只读库，不执行任何写操作，内置并发排队、算力冷却与熔断保护。

---

## 🚀 部署方式（二选一）

### 方式一：Docker Compose 容器部署（推荐）

将以下服务节点追加到你 Immich 现有的 `docker-compose.yml` 中的 `services:` 节点下：

```yaml
services:
  immich-searchplus2026:
    # 方式 A：直接拉取 GitHub 预构建镜像
    image: ghcr.io/<你的GitHub用户名>/immich-searchplus2026:latest
    # 方式 B：使用本地源码构建
    # build:
    #   context: ./mine-search
    #   dockerfile: Dockerfile
    container_name: immich-searchplus2026
    restart: always
    ports:
      - "1880:1880"
    environment:
      # 数据库连接 (直接复用 Immich 环境变量)
      - IMMICH_DB_HOST=database
      - IMMICH_DB_PORT=5432
      - IMMICH_DB_NAME=${DB_DATABASE_NAME:-immich}
      - IMMICH_DB_USER=${DB_USERNAME:-postgres}
      - IMMICH_DB_PASSWORD=${DB_PASSWORD}
      # Immich 核心微服务通信 (内部服务名解析)
      - IMMICH_SERVER_URL=http://immich-server:2283
      - IMMICH_ML_URL=http://immich-machine-learning:3003/predict
      # 机器学习模型 (必须与当前 Immich 后台实际运行的模型名称严格一致)
      - IMMICH_CLIP_MODEL=nllb-clip-large-siglip__v1
      - IMMICH_FACE_MODEL=antelopev2
      # 人脸 1:1 比对独立免密访问密钥 (选填，设置后可凭此 Key 免相册登录使用)
      # - IMMICH_COMPARE_API_KEY=your_custom_secret_key
      - TZ=Asia/Shanghai
    depends_on:
      - database
      - immich-machine-learning
      - immich-server
```

启动服务：

```bash
docker compose up -d immich-searchplus2026
```

---

### 方式二：直接运行独立 Linux x86_64 二进制

适用于不想起 Docker 容器、运行在轻量 LXC 容器或直接在宿主机裸跑的用户。本项目所有前端 UI 静态资源与胶水脚本均已打包嵌入单一二进制中，无任何外部文件依赖。

#### 1. 下载并提权

从 GitHub [Releases 页面](../../releases) 下载最新的 `server` 二进制文件，放置到目标目录（如 `/opt/searchplus2026`）：

```bash
mkdir -p /opt/searchplus2026 && cd /opt/searchplus2026
# 下载二进制并赋予执行权限
chmod +x server
```

#### 2. 创建环境变量配置文件

在同一目录下创建 `.env` 文件并填入你的 Immich 实际地址与数据库密码：

```bash
cat << 'EOF' > .env
PORT=1880
# 宿主机运行时连接地址（若 Immich 容器端口映射到宿主机，填写对应 IP/端口）
IMMICH_SERVER_URL=[http://127.0.0.1:2283](http://127.0.0.1:2283)
IMMICH_ML_URL=[http://127.0.0.1:3003/predict](http://127.0.0.1:3003/predict)
IMMICH_DB_HOST=127.0.0.1
IMMICH_DB_PORT=5432
IMMICH_DB_NAME=immich
IMMICH_DB_USER=postgres
IMMICH_DB_PASSWORD=your_actual_db_password
IMMICH_CLIP_MODEL=nllb-clip-large-siglip__v1
IMMICH_FACE_MODEL=antelopev2
TZ=Asia/Shanghai
EOF
```

#### 3. 命令行临时测试启动

```bash
export $(cat .env | xargs) && ./server
```

控制台输出 `[✓] Immich SearchPlus2026 服务已就绪，正在监听 :1880` 即表示启动成功。

#### 4. 配置 systemd 后台常驻与开机自启（生产推荐）

创建守护服务配置文件 `/etc/systemd/system/searchplus2026.service`：

```ini
[Unit]
Description=Immich SearchPlus2026 Service
After=network.target docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/searchplus2026
EnvironmentFile=/opt/searchplus2026/.env
ExecStart=/opt/searchplus2026/server
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

激活并启动服务：

```bash
systemctl daemon-reload
systemctl enable --now searchplus2026

# 查看运行状态
systemctl status searchplus2026
```

---

## 🌐 反向代理与脚本注入 (以 Nginx Proxy Manager 为例)

无论采用 Docker 还是独立二进制运行，只需在 NPM 对应 Immich 域名的 **Advanced**（高级规则）中加入以下内容，即可实现视口接管与胶水脚本自动加载：

```nginx
# 1. 禁用上游 gzip 压缩，确保 sub_filter 拦截替换生效
proxy_set_header Accept-Encoding "";

# 2. 转发 SearchPlus2026 搜图与比对接口
# (若使用 Docker 部署填容器名:1880；若使用二进制裸跑填 127.0.0.1:1880)
location /searchplus2026/ {
    proxy_pass http://immich-searchplus2026:1880/searchplus2026/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}

# 3. 在 Immich 主页 </body> 闭合前自动注入前端交互接管脚本
sub_filter '</body>' '<script src="/searchplus2026/inject.js"></script></body>';
sub_filter_once on;
sub_filter_types text/html;
```

---

## 📌 注意事项与常见问题

1. **模型名称严格一致**：
   `IMMICH_CLIP_MODEL` 与 `IMMICH_FACE_MODEL` 必须与 Immich 实例当前运行的模型名称完全相符，否则因向量特征空间不匹配会导致检索无结果。
2. **核显/GPU 显存保障**：
   AntelopeV2 人脸模型前向推理时瞬间显存峰值较高。若使用 Intel 核显（OpenVINO）时遇到 ML 容器 500 报错（`-14` 错误码），建议重启宿主机进入 BIOS，将核显共享显存（DVMT Pre-Allocated / UMA Frame Buffer Size）调高至 **512MB 或 1024MB**。
3. **独立人脸 1:1 比对工具**：
   访问 `https://your-immich-domain/searchplus2026/?app=compare` 即可进入专属的人脸比对模式。若设置了 `IMMICH_COMPARE_API_KEY`，使用者输入该密钥即可直接免登录比对人像。

---

## 📄 开源许可证

本项目代码采用 [MIT License](./LICENSE) 授权，所依赖的人脸识别模型权重遵循 InsightFace 原创作者的非商业性研究使用协议。
