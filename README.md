> 为自建 Immich 相册打造的以图搜图、人脸搜人、人脸相似度 1:1 比对的伴生服务。

## 这个项目让immich支持

1. 以用户选择的图片搜索相册内相似的图片，不用将图片上传到图库。
2. 以用户选择包含人脸的图片查找相册内的对应人，不用将图片上传到图库。
3. 附加功能还有对比两个人脸的相似度，不用将图片上传到图库。

【图片占位】
【图片占位】

## 重要说明

```
使用的前提条件：你现在能正常使用immich的机器学习检索照片
工作原理：直连 immich的数据库与Immich机器学习服务，实现以图搜图（CLIP）、人脸多目标检索与 1:1 特征比对。
使用方式： 可作为独立网页使用也可以内嵌到immich的网页当中
开发环境：以immich 3.2.2为开发基础
```

> [!WARNING]
> **免责声明与风险自担 (Disclaimer & Assumption of Risk)**
>
> 1. 本项目按 **“现状”（AS-IS）** 提供，不包含任何明示或暗示的担保。
> 2. 用户在部署、配置、直连数据库及使用过程中产生的**一切后果（包括但不限于照片资产损坏丢失、数据库崩溃、硬件异常过热、网络被侵入等），均由使用者 100% 自行完全承担**，作者与贡献者概不承担任何直接或间接的连带法律责任。
> 3. 自建相册珍贵数据无价，请在部署任何第三方扩展前，务必严格执行官方推荐的 **3-2-1 数据备份策略**！

---

## ✨ 核心特性

- 🖼️ **以图搜图 (CLIP)**：就是用图片搜索图片不需要上传到图库再查找相似啦~欢呼！
- 👤 **人脸多目标匹配 (InsightFace)**：自动识别人脸位置并支持多选，穿透检索相册内同一人物所有照片。
- 👥 **人脸 1:1 独立比对**：双样本余弦相似度极速比对，支持通过专属密钥免相册直通运行（访问 `?app=compare`）。
- ⚡ **原生视口接管**：通过轻量脚本无感嵌入 Immich 原生导航栏，支持移动端返回手势与相册时间线回退重定位。
- 🛡️ **纯只读旁路安全**：直连 pgvector 只读库，不执行任何写操作，内置并发排队、算力冷却与熔断保护。

---

## 🚀 部署方式（二选一）

### 方式一：直接运行独立 Linux x86_64 二进制

#### 1.下载二进制

下载二进制到一个文件夹

#### 2.创建环境变量文件.env 在同文件夹下

在同一目录下创建 `.env` 文件并填入你的 Immich 实际地址与数据库密码：

```
# =================================================================
# Immich 基础设施连接配置 (Docker Compose 内部服务名直连)
# =================================================================
IMMICH_SERVER_URL=http://immich-server:2283
IMMICH_ML_URL=http://immich-machine-learning:3003/predict
IMMICH_DB_HOST=database
IMMICH_DB_PORT=5432
IMMICH_DB_NAME=immich
IMMICH_DB_USER=postgres
IMMICH_DB_PASSWORD=your_database_password_here

# =================================================================
# 机器学习模型配置 (必须与 Immich 主服务正在运行的模型名称保持完全一致)
# =================================================================
IMMICH_CLIP_MODEL=nllb-clip-large-siglip__v1
IMMICH_FACE_MODEL=antelopev2

# =================================================================
# 服务运行参数与安全密钥
# =================================================================
PORT=1880

# 人脸 1:1 比对专属独立授权密钥 (选填，设置后可通过 ?app=compare 独立访问)
# IMMICH_COMPARE_API_KEY=your_custom_secret_key_here


```

也支持config.json config.yaml，已提供example文件，可通过 `-c` 指定配置文件。

### 3.运行这个二进制文件测试

### 方式二：Docker Compose 容器部署 （这个说明目前占位用，不要使用）

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
      - IMMICH_DB_HOST=database #数据库地址
      - IMMICH_DB_PORT=5432 #数据库端口
      - IMMICH_DB_NAME=${DB_DATABASE_NAME:-immich} #数据库名称
      - IMMICH_DB_USER=${DB_USERNAME:-postgres} #数据库用户名
      - IMMICH_DB_PASSWORD=${DB_PASSWORD} #数据库密码
      # Immich 核心微服务通信 (内部服务名解析)
      - IMMICH_SERVER_URL=http://immich-server:2283 #immich服务地址
      - IMMICH_ML_URL=http://immich-machine-learning:3003/predict #机器学习服务地址
      # 机器学习模型 (必须与当前 Immich 后台实际运行的模型名称严格一致)
      - IMMICH_CLIP_MODEL=nllb-clip-large-siglip__v1 #以文搜图的模型名
      - IMMICH_FACE_MODEL=antelopev2 #人脸识别的模型名
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

## 🌐实现将本功能附加到immich官方网页

### 反向代理与脚本注入 (以 Nginx Proxy Manager 为例)

无论采用 Docker 还是独立二进制运行，只需在 NPM 对应 Immich 域名的 **Advanced**（高级规则）中加入以下内容，即可实现视口接管与胶水脚本自动加载：

```nginx
# 1. 禁用上游 gzip 压缩，确保 sub_filter 拦截替换生效
proxy_set_header Accept-Encoding "";

# 2. 转发 SearchPlus2026 搜图与比对接口到https://你的immich域名/searchplus2026/
location /searchplus2026/ {
    proxy_pass http://immich-SearchPlus2026的服务地址:1880/;
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

### ⚠️你还可以把这个inject.js下载下来放到油猴脚本中运行，只需要添加油猴脚本专用的头部

### 💡这个脚本还帮immich 3.2.2修复了后退网页时网页被锁定无法滚轮滚动的bug，还有后退键不后退到当前预览图在时间线上所在位置的bug。

---

## 编译

### 一.用docker编译

#### 1.编译成docker镜像

在Dockerfile同目录下执行

```
docker build -t immich-searchplus2026:latest .
```

#### 2.编译输出二进制

在Dockerfile同目录下执行

```
DOCKER_BUILDKIT=1 docker build --target export-stage --output type=local,dest=./release .
```

编译到指定系统架构：（以windows amd64为例）

```
DOCKER_BUILDKIT=1 docker buildx build --target export-stage --output type=local,dest=./release --build-arg TARGETOS=windows --build-arg TARGETARCH=amd64 .
```

输出的文件在./release里

### 二.不用docker也能编译

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
