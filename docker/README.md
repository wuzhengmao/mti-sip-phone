# 容器化交叉编译

一个容器镜像产出**全部 10 个产物**：**完整版 × 5 平台 + 仅托盘无 UI 版（noui）× 5 平台**。

| 产物 | 工具链（镜像内已装） |
| --- | --- |
| `sip-phone-linux-amd64` | 本机 gcc + X11/GL/Wayland 开发包 |
| `sip-phone-linux-arm64` | `aarch64-linux-gnu-gcc/g++` + arm64 图形栈开发包 |
| `sip-phone-windows-amd64.exe` | `x86_64-w64-mingw32-gcc`（静态链接） |
| `sip-phone-windows-386.exe` | `i686-w64-mingw32-gcc`（静态链接） |
| `sip-phone-windows-arm64.exe` | llvm-mingw（基础版，见下） |
| `sip-phone-no-ui-<os>-<arch>` ×5 | 同上（noui 变体不链接 fyne/GLFW/OpenGL，**linux 侧不需要图形栈包**） |

## 基线：Debian 10 / glibc 2.28（镜像 `wuzm219/msp-builder`）

**信创环境的实测 glibc 底线**（这就是必须用 Debian 10 的原因）：

| 目标系统 | glibc |
| --- | --- |
| 统信 UOS 专业版（V20 各版本） | **2.28**（基于 Debian 10，官方明确不能单独升级 glibc） |
| 银河麒麟 V10 SP1 桌面（x64/arm64） | 2.31 |
| 麒麟（龙芯/申威平台） | 2.28 |
| deepin 23+ | 2.38 |

在 **2.28** 上编译的产物，在 glibc ≥ 2.28 的系统上都能运行（glibc 严格向后兼容）。
**注意三件事**：

1. **第三方静态库（视频/Opus/AEC）必须在"本镜像内"编译**（`docker/build.sh --libs`）——
   在更新宿主编的 `.a` 拿去低版本容器链接会直接报错（找不到高版本符号），
   好在这种失败是干脆的，不会静默产出坏产物。
2. 产物还依赖目标机的 **C++ 运行时**（`libstdc++.so.6`，本项目链了 OpenH264/abseil）——
   Debian 10 的 GCC 8.3 对应 `GLIBCXX_3.4.25`，比这新的信创系统都没问题。
3. **完整版**（有界面）依赖目标机的 X11/GL/Wayland 库；**无桌面/服务器场景请用 `noui` 产物**
   （不链接图形栈，依赖最少）。

**与 Windows 产物无关**：交叉编译到 Windows 时宿主 glibc 不参与，Windows 产物仍是
**Windows 10 及以上**（Go 1.22 起工具链的硬要求，`ProcessPrng` 等），换基线不影响。

### 为什么 llvm-mingw 钉在 20230320

llvm-mingw 官方的 Linux 包按构建宿主划分，要在 glibc 2.28 的镜像里能跑就只有这一档：

| 版本段 | 包基于 | 要求宿主 glibc |
| --- | --- | --- |
| ≤ 20230320 | ubuntu-18.04 | **≥ 2.27** ← 本镜像用它（clang 16） |
| 20230427 ~ 20250402 | ubuntu-20.04 | ≥ 2.31 |
| 20250417 起 | ubuntu-22.04 | ≥ 2.35 |

## 用法

**不需要 Docker Desktop**：WSL 3.0.1+ 自带的 **`wslc`**（WSL 原生容器）就能跑。脚本会自动
探测容器 CLI（优先 `wslc`，回退 `docker`）；命令在 Windows 的 git-bash 里执行（WSL/Linux
里用 docker 也一样）。脚本内部已处理两个 shell 侧的坑：git-bash 会把容器内路径参数自动
转成 Windows 路径（用 `MSYS_NO_PATHCONV` 关掉）、以及 `bash -l` 会被 `/etc/profile`
重置 PATH（用非登录 shell 跑）。

```bash
# 1) 首次：构建镜像（几分钟；Debian 10 已 EOL，apt 走 archive 源）
bash docker/build.sh --build-image

# 2) 首次：编第三方静态库（视频 / Opus+AEC）——很慢（几十分钟到数小时），只需一次
bash docker/build.sh --libs

# 3) 日常：编译全部 10 个产物（缺第三方库也能跑，对应功能降级为无视频/无 Opus/AEC）
bash docker/build.sh
```

产物出在仓库 `dist/`，第三方静态库在 `third_party/`（都在宿主仓库里，持久化）。
Go 的模块与构建缓存用容器命名卷（`mti-sip-phone-gomod` / `-gocache`），第二次起快很多。

其它命令：

```bash
bash docker/build.sh --shell        # 进容器排查（工具链自检、手工跑构建步骤）
bash docker/build.sh --go 1.25.3    # 换 Go 版本重建镜像
docker rmi wuzm219/msp-builder        # 彻底重来（下次自动重建）
```

## 性能提示（重要）

**仓库请放在 WSL 自己的文件系统里**（如 `~/git/mti-sip-phone`），不要在 `/mnt/d/...`
上直接编译：Windows 盘经 9p/virtiofs 挂进 WSL、再挂进容器是两层跨文件系统 IO，Go
编译大量小文件会慢一个数量级。从内网 GitLab 直接 clone 进 WSL 即可。

## 与 Windows 侧构建的分工

- **发版用的最终产物：以本容器为准**（Linux 产物 + 交叉 Windows 产物，基线可复现）。
- **windows-arm64**：容器里出的是**基础版**（无视频/Opus/AEC）；真机上还需装
  OpenGL 兼容包才能起界面（`WGL: The driver does not appear to support OpenGL` 是已知问题）。
- Windows 上的 `build.bat` 保留给**本机开发自测**。

## 与发布脚本的集成

`scripts/release.sh`（发版）**统一走本容器编译**——不再按平台分 build.bat / build.sh：
预发布与正式版都先跑 `docker/build.sh`（正式版把 `SIP_PHONE_RELEASE=1` 透传进容器），
再从 `dist/` 收集 10 个产物打包上传。

## 镜像分发（Docker Hub）

镜像发布在 **`wuzm219/msp-builder`**（`latest`）——换一台构建机 `pull` 下来就能编，
不必重建（约 3.5GB / 压缩传输 ~800MB）：

```bash
docker login                              # 首次：登录 Docker Hub 账号 wuzm219
docker pull wuzm219/msp-builder:latest    # 拉取（新机器）
# 改过 Dockerfile 或想刷新镜像后：
docker build --progress=plain -t wuzm219/msp-builder docker/   # 等价于 docker/build.sh --build-image
docker push wuzm219/msp-builder:latest
```

## 排查

| 症状 | 原因/处理 |
| --- | --- |
| `docker info` 失败 | Docker Desktop 没启动，或 WSL 集成没打开 |
| apt 报 `Release file expired` | 已由镜像内的 `Acquire::Check-Valid-Until=false` 处理；换基础镜像时记得带上 |
| 链接第三方库报 `undefined reference to ...@GLIBC_2.3x` | 静态库是在更高基线环境编的——重跑 `docker/build.sh --libs` 用本镜像重编 |
| `build.sh` 跳过某个目标并打印准备步骤 | 容器缺该目标的工具链——正常情况下镜像里已装齐，`--shell` 进去按提示核对 |
| 产物在宿主上属主是 root | `docker/build.sh` 会自动 chown 回来；手工 `docker run` 跑的没有 |
