# 自动更新与「更新站点」接入技术文档

> **面向读者**：要把自己的应用接入「软件更新中心」（下称"站点"）的开发同学，以及需要理解这条链路的 AI。
> **参考实现**：mti-sip-phone 1.0.5（Go + Fyne 桌面软电话，Windows/Linux）。读完本文、照着**第七节 接入清单**
> 走一遍，就能让另一个应用具备"发布 → 检测 → 提示 → 下载 → 替换重启"的完整链路；文末**附录**附了核心代码，
> 无需访问本项目仓库即可照搬实现。
>
> **站点在线地址**：`https://update.wuzm219.cn`。站点自身的**权威规范文档**也在这个站点上，直接浏览器打开：
>
> - 应用发布规范 v1：<https://update.wuzm219.cn/docs/release-spec>
> - 上传发布：<https://update.wuzm219.cn/docs/upload>
> - REST API：<https://update.wuzm219.cn/docs/api>
>
> 本文是**接入方视角**的整合版：把规范、接口、参考实现与踩过的坑串成一条线。与站点文档冲突时以站点文档为准。

---

## 一、总览

### 1.1 三个角色

| 角色 | 职责 | 本项目的对应物 |
| --- | --- | --- |
| **站点**（软件更新中心） | 存归档、索引、版本比较、分发；提供 REST API | 独立仓库部署，生产地址 `https://update.wuzm219.cn` |
| **发布方** | 编译 → 按规范打包 → 上传 | [`scripts/release.sh`](../scripts/release.sh)（+ `release/release.conf`） |
| **客户端** | **自己**查版本 → 提示用户 → 下载校验 → 替换自身 → 重启 | Go：`internal/updater`（查询 `checker.go` + 状态机）+ `internal/wsapi`；网页：只负责写配置与强制推送 |

### 1.2 两条链路

```
发布方（开发机 / CI）                                    客户端（应用本体）
  build（版本号注入）                             启动即查一次 / 之后按配置的周期查
      │                                                        │
  按规范打包（文件命名 + release.json + CHANGELOG + SHA256SUMS） │
      │                                                        │
  PUT  /api/v1/apps/<app>/metadata   （应用级信息）              │
  PUT  /api/v1/apps/<app>/icon       （图标）                    │
  POST /api/v1/apps/<app>/upload     （发布包，tar.gz/zip）  ─┐  │
      │                                                      │  │
      └──────────────────────────────────────────────────►  更新站点
                                                             │  │
                                        GET  /api/v1/apps/<app>/check?version=&os=&arch=
                                                             │  │
                                          updateAvailable? ──┘  │
                                                │                │
                                          提示用户确认（本项目：必须用户点）│
                                                │                │
                                        GET /dl/<app>/<版本>/<文件>（含 sha256 头）
                                                │
                                        校验 sha256 + 可执行魔数 → 替换自身 → 重启
```

**一条重要边界**：站点只负责"有没有更高版本、产物在哪"；**是否下载、是否重启永远由客户端决定**。
查询由客户端自己发起（本项目 = 软电话，配置见 REQ-UPD-017；网页可用 `system.setUpdateConfig` 写这份配置）。
本项目的红线是"下载必须由用户在应用界面上点击"（`REQ-UPD-009`），WS 接口只能"告知"。

### 1.3 最短路径

一个应用要接入站点，只需要做三件事：

1. **发布**：按 `apps/<app-id>/<版本号>/` 的规范打包，用令牌上传（或让管理员把文件放进归档目录）。
2. **检测**：`GET /api/v1/apps/<app>/check?version=<当前版本>&os=&arch=`，看 `updateAvailable` 与 `download`。
3. **安装**：下载 `download.url`，用 `download.sha256` 校验，替换自身并重启。

其余（通道、预发布、元数据接口、图标）都是增强项。

---

## 二、站点侧：发布规范（做对就能被识别）

> 规范原文：站点 `/docs/release-spec`。这里摘录接入方必须知道的部分。

### 2.1 目录结构（目录名即版本号）

```
<DATA_DIR>/apps/                      # 容器的 /data/apps，宿主机上的归档目录
└── <app-id>/                         # 应用 ID，同时是 URL 里的标识
    ├── app.json                      # 可选：应用级信息（名称/简介/标签/图标…）
    ├── icon.svg                      # 可选：图标（也支持 icon.png / logo.png / logo.svg / icon.jpg）
    └── 1.0.5/                        # 版本目录，目录名就是版本号
        ├── release.json              # 可选：版本级信息
        ├── CHANGELOG.md              # 可选：更新说明（网页与 API 都读它）
        ├── MyApp-windows-x64.exe     # 产物（文件名即平台/架构线索）
        └── SHA256SUMS                # 可选：校验和清单
```

- **最小可用发布** = 建目录 + 放文件。扫描器默认 15 秒一轮，自动识别。
- 版本目录名必须匹配 `^v?\d+(\.\d+)*([-_+][0-9A-Za-z.\-+]*)?$`（如 `1.0.5`、`2.0.0-rc.1`、`2026.10.03`）。
- 以 `.` 开头的文件/目录一律忽略（解压中途的 `.staging-*` 因此不会被外部看到）。
- 应用 ID 允许 `a-z A-Z 0-9 . _ -`，不能以 `.` 开头。

### 2.2 `app.json`（应用级，可选）

```json
{
  "name": "mti-sip-phone 软电话",
  "summary": "一句话描述",
  "description": "详细描述，支持 Markdown",
  "vendor": "MTI",
  "license": "Proprietary",
  "tags": ["desktop", "sip"],
  "channel": "stable",
  "order": 10
}
```

| 字段 | 说明 |
| --- | --- |
| `name` / `summary` / `description` | 显示名、一句话、详情（Markdown，最长 20000） |
| `vendor` / `license` / `tags` | 厂商、许可、标签（≤30 个） |
| `channel` | 默认发布通道（默认 `stable`），也是 `check` 不带 `channel` 时的默认值 |
| `order` | 列表排序，小的靠前（默认 100） |
| `hidden` | `true` 时不在列表展示，但下载地址仍有效 |
| `icon` | 图标文件名，**由图标接口维护**，别手写 |

**推荐做法**：用站点的元数据接口（`PUT /metadata`、`PUT /icon`）写入，不要手改归档目录里的
`app.json` —— 接口会做校验并触发重扫（详见 3.3）。

### 2.3 `release.json`（版本级，可选）+ `CHANGELOG.md`

脚本通常自动生成：

```json
{
  "channel": "stable",
  "title": "mti-sip-phone 1.0.5",
  "publishedAt": "2026-10-03T16:00:00Z",
  "prerelease": false,
  "mandatory": false,
  "artifacts": [
    {
      "file": "mti-sip-phone-windows-x64.exe",
      "name": "Windows 64 位（免安装）",
      "kind": "portable",
      "os": "windows",
      "arch": "x64",
      "sha256": "9d0503aa…",
      "size": 39652864
    }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `prerelease` | 版本号含 `-` 或 `_` 时**默认 true**；`check` 默认不返回预发布版 |
| `mandatory` | 强制更新标记，API 原样返回给客户端自行决策 |
| `minVersion` | 低于该版本不支持增量升级，客户端应引导全量安装 |
| `artifacts[]` | 声明 `file/name/kind/os/arch/sha256/size`，**声明的顺序即展示顺序**；没声明的文件仍会被自动收录 |
| `notes` | Markdown 更新说明，会被同目录的 `CHANGELOG.md` **覆盖** |

- 更新说明优先级：`CHANGELOG.md` > `changelog.md`/`CHANGES.md` > `RELEASE_NOTES.md` > `NOTES.md` > `release.json.notes`。
- 校验和优先级：`release.json.sha256` > 同名旁挂 `<文件名>.sha256` > `SHA256SUMS` 清单 > 站点自己算。
- `version` 字段可写但被忽略——**目录名永远是权威**。

### 2.4 文件名与平台识别

站点从文件名推断平台/架构/类型（识别词可出现在任意位置），也能被 `release.json` 显式声明覆盖。
建议命名：`<产品名>-<版本号>-<平台>-<架构>.<扩展名>`。

**本项目采用"文件名固定、不含版本号"**（`mti-sip-phone-windows-x64.exe`）：

- 好处：同一平台的下载地址跨版本不变（`/dl/<app>/latest/<文件>` 与固定版本号 URL 都能长期收藏）。
- 代价：必须靠 `release.json` 的 `artifacts[].os/arch` 显式声明（否则站点只能从文件名猜，虽然也能猜对）。
- 规范允许这种命名（"只要文件名里出现平台和架构词，站点仍能识别"）。

平台/架构别名（接受的写法，大小写不敏感）：

| 规范 id | 常见别名 |
| --- | --- |
| `windows` | win / win32 / win64 / mingw / cygwin |
| `linux` | gnu / linuxgnu |
| `macos` | mac / osx / darwin |
| `x64` | amd64 / x86_64 / x8664 / 64 |
| `x86` | i386 / i686 / 386 / win32 / ia32 |
| `arm64` | aarch64 / armv8 |
| `arm32` | arm / armv7 / armhf |
| `universal` | noarch / all / any |

> 客户端上报 `runtime.GOARCH`（`amd64`/`386`/`arm64`）即可，站点会归一化匹配。

### 2.5 通道与下载路径

- **通道**：一个应用可有多个通道（`stable` / `beta`…），通道写在 `release.json.channel`（默认取 `app.json.channel`，再默认 `stable`）。查询时 `?channel=beta`。
- **下载**：`/dl/<app-id>/<版本号>/<文件名>`（长期有效）、`/dl/<app-id>/latest/<文件名>`（解析到该通道最新版）。文件名可只写文件名，大小写不敏感。
- `latest` = 该通道内**版本号最高**的版本；预发布版按语义化版本天然低于同号正式版（`1.0.5-rc < 1.0.5`）。

---

## 三、站点侧：REST API

### 3.1 自动更新用这两个就够

#### `GET /api/v1/apps/{app}/check`

| 参数 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `version` | **是** | — | 客户端当前版本（缺省返回 400） |
| `os` / `arch` | 否 | — | 客户端平台/架构（别名可），用于挑出该平台的最佳产物 |
| `channel` | 否 | 应用默认通道 | 通道名 |
| `prerelease` | 否 | **false** | 是否接受预发布版 |

响应（要点）：

```json
{
  "app": "mti-sip-phone", "name": "mti-sip-phone 软电话",
  "currentVersion": "1.0.4", "channel": "stable", "os": "windows", "arch": "x64",
  "updateAvailable": true, "upToDate": false,
  "latestVersion": "1.0.5", "mandatory": false,
  "notes": "### 新增\n…",
  "download": {
    "file": "mti-sip-phone-windows-x64.exe",
    "url": "https://update.wuzm219.cn/dl/mti-sip-phone/1.0.5/mti-sip-phone-windows-x64.exe",
    "path": "/dl/mti-sip-phone/1.0.5/mti-sip-phone-windows-x64.exe",
    "sha256": "9d0503aa…", "size": 39652864,
    "os": "windows", "arch": "x64", "kind": "portable", "available": true
  }
}
```

- **判定规则**：客户端版本不低于通道内最新版本 → `upToDate: true` 且不返回 `download`；否则 `updateAvailable: true`。
- 默认 `prerelease=false`：**只发预发布版的通道会返回 up-to-date**，避免自动更新把正式用户带到候选版。
- 该平台没有产物时 `download` 为 `null`（客户端应视为"暂不更新"而不是报错）。
- 站点不可用/应用不存在：404（错误体 `{"error":{"code":…}}`）。

#### `GET /api/v1/apps/{app}/latest`

按平台取"最新版本"，用来做"手动检查更新"列表（本项目测试页用它列出**最新版本**与**最新稳定版**）：

```
GET /api/v1/apps/mti-sip-phone/latest?os=windows&arch=amd64&prerelease=true    # 最新版本（含预发布）
GET /api/v1/apps/mti-sip-phone/latest?os=windows&arch=amd64&prerelease=false   # 最新稳定版
```

返回 `{app, channel, release:{…}, download:{…}}`；该通道**没有任何匹配版本**时返回 **404 `no_release`**
（客户端应把它当成"没有"，不是错误）。

#### `GET /dl/{app}/{version}/{file}`（下载）

响应头对客户端很重要：

| 头 | 用途 |
| --- | --- |
| `X-Checksum-Sha256` | 完整校验和，可与 `check` 返回的 `sha256` 比对 |
| `ETag: "sha256-<hex>"` | 支持 `If-None-Match` 返回 304 |
| `Accept-Ranges: bytes` | 支持断点续传 |
| `Content-Disposition` / `Content-Type` | 按扩展名给出准确类型 |

### 3.2 发布侧接口

| 接口 | 说明 |
| --- | --- |
| `POST /api/v1/apps/{app}/upload` | 上传发布包（zip / tar / tar.gz / tar.bz2，**按文件头识别，不看扩展名**；不支持 tar.xz/7z/rar）。multipart 带 `file` + 可选 `version`，或直接把归档作为 body + `?version=`。成功 201，同名版本**整体替换**（`replaced: true`） |
| `POST /api/v1/rescan` | 立即触发扫描（默认 15 秒一轮）。站点设了 `RESCAN_TOKEN` 时要带 |

发布包的**内容**是"一个版本目录里的东西"（不要带应用目录那一层）；站点会自动丢弃 `__MACOSX/`、
`.DS_Store`、以 `.` 开头的项、符号链接/硬链接/设备文件，并要求包里**至少有一个可安装文件**
（只放 `release.json` + `CHANGELOG.md` 会被拒）。

### 3.3 应用级元数据接口（推荐用它们，而不是直接改归档目录）

| 接口 | 说明 |
| --- | --- |
| `GET /api/v1/apps/{app}/metadata` | 返回**原样的 app.json**（不是合并视图），供表单预填 |
| `PUT /api/v1/apps/{app}/metadata` | 保存 app.json。**整份替换**：没写的字段会被清空；未知字段直接 400（防拼错字段静默丢数据）。`icon` 字段由图标接口维护，别手写 |
| `PUT /api/v1/apps/{app}/icon` | body 是图标二进制（PNG/JPEG/SVG/WebP，≤2 MB）。**类型按内容判定**，文件名由站点决定（`icon.svg`/`icon.png`…），写入后会删掉同目录其他图标名并把 app.json 的 `icon` 指向新文件 |

三个接口与上传**共用同一枚令牌**；站点没配 `UPLOAD_SECRET` 时统一 403。

### 3.4 鉴权：上传令牌怎么来

令牌**由应用 ID 单独推导**（密钥是站点程序里编译进 `UPLOAD_SECRET` 的），同一个应用 ID 在任何站点、
任何环境算出来的令牌都一样。管理员一条命令即可为每个应用打印令牌：

```bash
# 容器里（镜像无 shell，但二进制在 PATH 上）
docker exec updatesite updatesite token <app-id>          # 打印令牌 + 可粘贴的 curl
docker exec updatesite updatesite token <app-id> -q       # 只要令牌
docker exec updatesite updatesite token --list            # 列出归档目录里所有应用的令牌
```

用法：`Authorization: Bearer <令牌>` 或 `X-Upload-Token: <令牌>`。

> **安全边界（站点文档原话的意思）**：令牌的推导密钥编译在程序里，所以**拿到站点程序/源码的人可以为任意应用算出令牌**。
> 它能挡住扫描器与泄漏的 URL，挡不住拿到程序本体的人。要更强的隔离就关掉上传（`UPLOAD_ENABLED=false`），只用挂载目录发布。
> 也正因如此，本项目把令牌直接写进了 `release/release.conf`（团队内部仓库，用户确认），换站点时同步替换即可。

### 3.5 错误码

| 状态码 | 场景 |
| --- | --- |
| `400` | 应用 ID 非法 / 版本号无法确定 / 归档损坏或不含可安装文件 / 元数据含未知字段或超限 / 图标不是可识别图片 |
| `401` | 令牌缺失或错误 |
| `403` | 站点关闭了上传（未配置 `UPLOAD_SECRET`） |
| `404` | 应用/版本不存在；`/latest` 无匹配版本（`no_release`） |
| `413` | 超过 `MAX_UPLOAD`（默认 2 GiB）或图标超过 2 MB |
| `503` | 归档目录不可写（挂载成只读？） |

---

## 四、客户端接入：三种成熟度

### 方式 A：只做"能被更新"（最小）

应用内不做任何网络请求，只保证产物按规范发布；用户从站点页面手动下载。
**适用**：内部工具、更新频率低、没有前端能力。

### 方式 B：应用自己查、自己装

应用启动后（或定时）调用 `check`，自己提示、下载、校验、替换、重启。要点：

```go
// 1) 查
u := fmt.Sprintf("%s/api/v1/apps/%s/check?version=%s&os=%s&arch=%s&prerelease=false",
    site, appID, url.QueryEscape(curVersion), runtime.GOOS, runtime.GOARCH)
var r struct {
    UpdateAvailable bool   `json:"updateAvailable"`
    LatestVersion   string `json:"latestVersion"`
    Mandatory       bool   `json:"mandatory"`
    Notes           string `json:"notes"`
    Download        *struct {
        URL, SHA256 string
        Size        int64
    } `json:"download"`
}
// 2) 有新版本且 download != null → 提示用户（不要静默安装）
// 3) 用户确认后：下载到临时文件 → sha256 校验 → 校验可执行魔数（PE/ELF）→
//    替换自身（先改名为 .old 备份，失败回滚）→ 重启（走正常退出路径）
```

细节建议（本项目在 `internal/updater` 里的做法）：

- **先探测目录可写**再下载，避免下完才发现装不上；
- **魔数校验**：Windows 校验 `MZ` + PE 签名，Linux 校验 ELF 魔数——防止把错误页/压缩包当程序替换上去；
- **大小上限**（本项目 256 MB），并同时看 `Content-Length` 与累计字节数；
- **下载中可取消**；取消要回到"可重试"状态而不是失败态；
- **重启时序**：请求重启只做标记并取消主 ctx，**释放单实例锁/拉起新进程必须放在退出收尾**
  （先关 WS/SIP 监听、再释放锁、最后拉起新进程），否则新进程会因锁冲突自行退出、或因端口被占而改配置；
- **有通话/关键任务时不要强装**：本项目等通话结束后自动执行。

### 方式 C：应用自己查（本项目现做法）+ 业务前端可覆盖配置

**本项目 1.0.10 起**：查询由**应用自己**做（方式 B），但更新策略（站点地址、应用 ID、
检查周期、是否接受预览版）放在**应用配置**里，配置页可改、网页也可经 WS 写入
（业务前端统一管理）：

- 配置页「自动更新」卡片：更新站点（留空 = 不检查）、应用 ID、检查周期（默认 5 分钟）、接受预览版；
- WS `system.setUpdateConfig`（字段可选）→ 落盘 + 立即生效 + 广播 `system.updateConfig`；
- JS SDK `setUpdateConfig(cfg)`（构造参数同名项在连上后自动写入一次）；
- **SDK 不再查询站点**，只保留 `notifyUpdate`（强制推送，联调/回滚用）。

好处：**页面没打开也会检查更新**（桌面软电话本来就是常驻的）；策略仍可由业务前端下发。
代价：站点地址进了客户端配置（不再是"只留在网页侧"）。

> 历史做法（1.0.9 及以前）：方式 C 原指"应用完全不查，全靠网页 SDK 定时查并告知"，
> 代价是页面没打开就不会检查；该实现已随 REQ-UPD-019 移除。

### 方式 B/C 都要遵守的两条

1. **永不静默安装**：只提示，用户确认才动手（本项目 `REQ-UPD-009` 的硬要求）。
2. **版本比较要支持预发布**：`1.0.5-rc < 1.0.5`，否则"开发版收不到正式版更新通知"。
   规则同站点（`-`/`_` 引入预发布标识，逐段比较；`+` 之后是构建元数据不参与；无法解析的版本一律放行）。

---

## 五、本项目的参考实现（1.0.10）

> 代码位置：`internal/updater`（下载/替换/状态机）、`internal/wsapi`（WS 接口）、`internal/core/update.go`（类型）、
> `web/sdk/sip-phone-sdk.js`（网页 SDK）、`web/test.html`（测试页）、`scripts/release.sh`（发布）、`release/release.conf`（配置）。

### 5.1 版本号约定

| 概念 | 位置 / 规则 |
| --- | --- |
| 基础版本号（唯一真相源） | `internal/core/buildinfo.go` 的 `var Version = "1.0.5"`，脚本**只读不写**，升版须人工确认 |
| 开发构建 | `build.bat` / `build.sh` 注入 `1.0.5-rc`（非正式版）——这样即使之后推正式版 `1.0.5`，数字相同也会通知更新 |
| 正式构建 | `SIP_PHONE_RELEASE=1` 时注入 `1.0.5`（发布脚本自动设置） |
| 比较规则 | `internal/updater/version.go` 的 `compareVersions`：数字段逐段比；预发布低于同号正式版；不可比则放行（裁决权在客户端/前端） |

> 新应用接入时建议照抄这套：**开发版带 `-rc`、正式版不带**；发布脚本按环境变量切换。

### 5.2 发布脚本（`scripts/release.sh`）

```bash
./scripts/release.sh --pre --dry-run   # 只编译打包（1.0.6-rc），不上传
./scripts/release.sh --pre             # 预发布：上传并标记为预发布
./scripts/release.sh                   # 正式版：SIP_PHONE_RELEASE=1 编译并上传
# Windows 用 scripts/release.bat（同样的参数，内部走 git-bash）
# 其他选项：-v <版本> / --title / --channel / --mandatory / --no-build
```

流程（每一步都有失败即中止的检查）：

1. **读配置**（`release/release.conf`：`UPDATE_SITE` / `APP_ID` / `UPLOAD_TOKEN`，可选 `CA_CERT` / `INSECURE_TLS`；同名环境变量可覆盖）。
2. **定版本**：`--pre` → `<基础版本>-rc`，否则基础版本；校验版本号格式（防路径穿越）。
3. **编译**：Windows 走 `cmd /c build.bat`（Linux 走 `build.sh`），正式版设 `SIP_PHONE_RELEASE=1`；
   记录构建开始时间供下一步用。
4. **收集产物**：只收托盘 GUI 产物（排除 console 版，避免同平台两个候选）；
   **mtime 早于本次构建开始的产物一律跳过并告警**（防止把上次的旧二进制当新版本发出去）；
   缺平台（如 Windows 上产不出 Linux 产物）显式列出。
5. **打包**：产物重命名为 `<APP_ID>-<平台>-<架构>[.exe]`；生成 `release.json`（显式声明 os/arch/kind/sha256/size）、
   `SHA256SUMS`；从根 `changelog.md` 抽取 `## [<版本>]` 段写 `CHANGELOG.md`（预发布版回落到基础版本段）。
6. **打成 tar.gz**（git-bash 没有 zip；站点按文件头识别格式）。
7. **写应用级元数据**：`release/app.json` → `PUT …/metadata`；`release/icon.svg` → `PUT …/icon`。
8. **上传发布包**：`POST …/upload`（`-F file=@… -F version=…`），显示进度条；201 成功，
   其他状态码打印服务端错误并退出非 0。
9. **核对**：`GET …/check?version=<基础版本>&os=windows&arch=amd64&prerelease=true` 看新版本是否可见。

配置长这样（`release/release.conf`）：

```ini
UPDATE_SITE=https://update.wuzm219.cn
APP_ID=mti-sip-phone
UPLOAD_TOKEN=<令牌>   # 找站点管理员执行 updatesite token <应用ID> 打印；每个应用一枚
# 内部自签名站点才需要：
#CA_CERT=D:/git/updatesite/certs/server.crt
#INSECURE_TLS=
```

**给新应用复用时**改三处：`APP_ID`（新应用 ID，同时决定归档目录名）、`UPLOAD_TOKEN`（找管理员算）、
脚本里第 4/5 步的产物列表与命名（各应用产物名不同），其余逻辑通用。

### 5.3 运行时链路（软电话侧）

```
配置：更新站点/周期/预览版（配置页，或网页 WS system.setUpdateConfig）
      │
      └─► internal/updater/checker.go 定时 GET /api/v1/apps/<app>/check
                    │  查到更高版本
                    ▼
          internal/updater.Notify（校验参数/平台/版本高低）
                                                              │
                          system.update 事件 ──► UI 右下角提示窗 + 主窗体提示行
                                                              │
                                        用户点「立即更新」 ──► Download
                                                              │
             写 <exe>.new.tmp → 进度 → sha256 校验 → 魔数校验 → 改名 <exe>.new
                                                              │
                              Apply：exe → .old 备份，.new → exe（失败回滚）→ 重启
```

关键点：

- **状态机**：`idle / available / downloading / ready / failed`，全部经 `system.update` 事件广播给 UI 与网页。
- **参数校验**：`version`/`url` 必填、仅 http/https、`os`/`arch` 提供时必须与本机一致。
- **版本高低**：`version` 不高于当前 → `update_not_newer`（前端视为正常结果）；
  新增可选 **`force: true`** 跳过这条检查（**强制推送**，供测试页「推送」按钮与回滚场景用；
  自动更新的定时查询**不带**它）。
- **重复告知**幂等；下载中/已就绪时同版本重复告知直接忽略。
- **下载**：上限 256 MB、sha256 比对（提供时）、PE/ELF 魔数校验、可取消。
- **替换**：Windows 上运行中的 exe **能改名不能覆盖** → `exe → .old`、`.new → exe`，失败回滚；
  启动时清理 `.old/.new/.new.tmp` 残留。
- **重启**：请求重启只标记并取消 ctx；**释放单实例锁与拉起新进程放在退出收尾**（先关监听、注销 SIP）。
- **UI**：右下角提示窗（不占任务栏、绿色话机图标），显示新版本/当前版本/下载来源主机名/进度；
  通话中先不弹（避免与通话面板抢右下角），通话结束补弹；同版本本次运行只弹一次。

WS 接口（详见 `docs/ws-protocol.md`）：

| 方法 | 请求 | 说明 |
| --- | --- | --- |
| `system.getState` | `{}` | 返回 `version/os/arch` 等，供前端查版本 |
| `system.notifyUpdate` | `{version, url, sha256?, notes?, os?, arch?, force?}` | 告知新版本；`force` 跳过版本高低检查 |

### 5.4 网页 SDK（`web/sdk/sip-phone-sdk.js`）

与更新相关的只剩两件事（**不再查询站点**，REQ-UPD-019）：

- `setUpdateConfig({url, appId, intervalMinutes, prerelease})` → WS `system.setUpdateConfig`
  （字段可选，只改传入的）；构造参数 `updateUrl`/`updateAppId`/`updateIntervalMinutes`/
  `updatePrerelease` 在连上软电话后自动写入一次；
- `notifyUpdate({version, url, sha256?, notes?})` → WS `system.notifyUpdate`，**恒为强制推送**
  （不受版本高低与预览版约束），供页面手动推送/联调。

### 5.5 测试页（`web/test.html`，随二进制内嵌）

「自动更新」卡片保留联调能力，但**查询改由页面自己 `fetch` 站点接口**（`/latest` 两次，
分别显式带 `prerelease=true` / `prerelease=false`），列出「最新版本」与「最新稳定版」，
每行一个黄色「推送」按钮（走 `sdk.notifyUpdate`）；另有「写入软电话配置」按钮把卡片里的
站点/应用 ID/周期/预览版写进软电话，之后由软电话自己定时检查。

### 5.6 需求条目索引

| 编号 | 内容 |
| --- | --- |
| `REQ-UPD-001`~`009` | 版本上报、`system.notifyUpdate`、提示窗、下载校验、替换重启、残留清理、`system.update` 事件、SDK 方法、更新源安全（下载必须用户点） |
| `REQ-UPD-010`~`016` | 定时查询站点（现由软电话自己做）、容错、预览版策略、测试页改造、**强制推送**、更新提示窗不占任务栏 |
| `REQ-UPD-017`~`019` | 软电话自检（配置页四项）、网页写入配置（`system.setUpdateConfig`）、SDK 只保留强制推送 |
| `REQ-REL-001`~`006` | 版本号约定（-rc）、预发布比较、一键发布脚本、发布物内容、元数据走接口、产物新鲜度校验 |

完整表述见 [`docs/requirements.md`](requirements.md)；站点接入相关的约定同步记录在 [`changelog.md`](../changelog.md) 的 `[1.0.5]` 段。

---

## 六、坑与设计决策（新应用接入前请读）

| 主题 | 结论 |
| --- | --- |
| **连接地址必须用 https** | 站点把 HTTP 端口 301 跳到 HTTPS，而**上传是 POST**——curl 跟随 301 会把 POST 退化成 GET（表现为莫名 301/400）。发布脚本里直接写 `https://` 地址，不要依赖跳转 |
| **自签名证书（内部站点）** | 浏览器里的 `fetch`/SDK 查询会被证书拦下（服务端日志无痕，现象是"查不到更新"）；要么把站点证书装进系统/浏览器信任库，要么只走 curl 侧发布（curl 可用 `--cacert` 指定证书） |
| **`download.url` 指向 localhost** | 那是站点侧 `BASE_URL` 没配成对外地址的表现，不是客户端问题——把 `check` 的原始返回发给站点管理员处理 |
| **令牌的定位** | 由 `UPLOAD_SECRET` 推导、按应用 ID 分发；能挡扫描器，挡不住拿到站点程序的人。别把 `UPLOAD_SECRET` 本身发给接入方 |
| **预发布语义** | `check` 默认不返回预发布 → 正式用户不会被带到候选版；开发版要收到正式版通知，客户端比较必须支持 `-rc < 正式版` |
| **强制推送** | 只作为"人工推送"的旁路（测试/回滚），自动更新链路一律走版本比较 |
| **文件名不含版本号** | 下载地址跨版本稳定；代价是要在 `release.json` 里显式声明 os/arch（本项目取舍） |
| **产物新鲜度** | 发布脚本按 mtime 跳过旧二进制：一键构建在 Windows 上产不出 Linux 产物，靠这条防止误发旧包 |
| **同目录两个候选** | 同一平台不要放两个可执行产物（如 GUI 版 + console 版）：站点挑"最佳产物"时两者会互相竞争（同类匹配取先声明/先发现的那个），用户拿到哪个不好预期——本项目的 console 版因此刻意不发布 |
| **查询的三条工程约束** | ① **启动后要立即查一次**（别等一个周期，否则刚填完站点五分钟内什么都没发生）；② 首次查询常常早于界面订阅事件——**界面起来后要补取一次状态**，否则提示窗永远不弹（同版本还会被去重挡掉）；③ 查询结果**不要拿站点的 os/arch 回灌**给客户端做平台校验（站点给的是 `x64/x86`，按 `GOARCH` 严格比对会拒绝） |
| **不静默安装** | 只用 `check` 做提示，安装必须用户确认；WS/前端只能"告知" |
| **桌面客户端的窗口细节** | 更新提示窗不占任务栏、图标用绿色话机（红色只作异常提醒）——这些与站点无关，但同类客户端常踩 |

---

## 七、接入清单（照着做）

1. **申请应用 ID**：给站点管理员一个稳定的 `<app-id>`（如 `myapp`），它会成为归档目录名与 URL 标识。
2. **拿令牌**：管理员执行 `updatesite token <app-id>` 打印令牌，只把**令牌**发给开发者（不给 `UPLOAD_SECRET`）。
3. **定版本号与命名**：确定基础版本号（如 `1.2.0`）与产物文件名（建议 `<产品名>-<平台>-<架构>.<扩展名>`；
   若想下载地址跨版本稳定，可不含版本号，但要在 `release.json` 里声明 os/arch）。
4. **实现发布脚本**：参考 `scripts/release.sh`——编译（注入正式/开发版本号）→ 打包（产物 + `release.json` +
   `CHANGELOG.md` + `SHA256SUMS`）→ `PUT metadata`/`PUT icon` → `POST upload`。
5. **首次发布**：先发一个 `-rc` 或 `1.0.0`，确认站点页面/接口能看到、能下载。
6. **客户端实现 `check`**：带上 `version/os/arch`，处理 `updateAvailable=false`、`download=null`、404；
   把"是否有更高版本"的判定权交给站点（自己别再算一遍）。
7. **实现安装流程**（方式 B）或**接入前端 SDK**（方式 C）：下载 → sha256/魔数校验 → 替换（备份+回滚）→ 重启；
   全程**用户确认**，可取消、可重试。
8. **开发期版本带 `-rc`**：保证正式版发布时开发机上的版本也能收到通知。
9. **联调**：用 `check` 手测四个场景——有更新、已是最新、本平台无产物（`download=null`）、站点 404。
10. **发正式版**：改版本号 → 更新 changelog → `./scripts/release.sh`（正式模式）→ 用
    `check?version=<上一版本>` 核对 `updateAvailable=true`、`download.url` 指向公网地址、`sha256` 一致。

> 站点的三份规范文档（`/docs/release-spec`、`/docs/upload`、`/docs/api`）是**权威**；
> 本文与它们冲突时以站点文档为准，并回来修本文。


---

## 附录：核心代码（可直接照搬）

> 以下代码从 mti-sip-phone 1.0.10 摘出（改代码后请顺手同步这里）：**一键发布脚本**、**Go 侧更新客户端**、**WS 接口**、
> **网页 SDK 的更新部分**与**测试页**。它们引用本项目的 `internal/logx`、`internal/core` 等包，
> **不能原样编译**，但逻辑、边界条件与注释都可以直接照搬/改写。路径是原仓库位置，仅作对照。

### 附录 A：一键发布脚本 `scripts/release.sh`（完整）

```bash
#!/usr/bin/env bash
# 允许用 sh(dash) 调用：本脚本用了 bash 专有语法（数组），dash 会在解析到那些
# 行时报 Syntax error。dash 是逐条执行顶层命令的，这条守卫会在读到后面的
# bash 语法之前先 exec 切回 bash。
if [ -z "${BASH_VERSION:-}" ]; then
    exec bash "$0" "$@"
fi
# 一键发布脚本：编译 → 按更新站点《应用发布规范 v1》打包 → 上传到更新站点
#
# 用法（Windows 走 git-bash，或直接 scripts\release.bat）：
#   ./scripts/release.sh                  # 正式版：版本取自 buildinfo.go（如 1.0.5）
#   ./scripts/release.sh --pre            # 预发布/开发版：版本 1.0.5-rc（站点标记为预发布）
#   ./scripts/release.sh --pre --dry-run  # 只编译打包，不上传（产物留在 dist/release/）
#   ./scripts/release.sh --no-build       # 用 dist/ 里已有的产物打包（跳过新鲜度校验）
#   ./scripts/release.sh --title "体验优化版" --mandatory
#
# 配置：release/release.conf（站点地址 / 应用 ID / 上传令牌 / 可选站点证书），
#       同名环境变量可覆盖（UPDATE_SITE / APP_ID / UPLOAD_TOKEN / CA_CERT /
#       INSECURE_TLS）。站点启用 TLS 时 UPDATE_SITE 必须直接写 https 地址
#       （HTTP 端口会 301 跳转，而 POST 跟随 301 会退化成 GET）。
#
# 产物打包内容（站点规范）：
#   mti-sip-phone-<平台>-<架构>[.exe]          发布产物（**不含版本号**：下载地址跨版本不变）
#   release.json                              版本元数据（artifacts 显式声明平台/架构/校验和）
#   CHANGELOG.md                              从根 changelog.md 抽取该版本段落
#   SHA256SUMS                                校验和清单
# 应用级元数据走站点接口，不直接动归档目录：
#   release/app.json  --PUT-->  /api/v1/apps/<app>/metadata     （整份替换）
#   release/icon.svg  --PUT-->  /api/v1/apps/<app>/icon         （文件名由站点按内容决定）
#
# 注意：
#   - Windows 上一键只能产出 Windows 产物（Linux 产物由 Linux 机器上的 build.sh 产出）；
#   - 构建产物的 mtime 必须晚于本次构建开始，否则视为旧产物跳过（防把上次的二进制当新版本发出去）；
#   - 版本号只读不写，升版仍需人工改 internal/core/buildinfo.go。
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
CONF="$ROOT/release/release.conf"

usage() {
    cat <<'EOF'
用法（Windows 走 git-bash，或直接 scripts\release.bat）：
  ./scripts/release.sh                  # 正式版：版本取自 buildinfo.go（如 1.0.5）
  ./scripts/release.sh --pre            # 预发布/开发版：版本 1.0.5-rc（站点标记为预发布）
  ./scripts/release.sh --pre --dry-run  # 只编译打包，不上传（产物留在 dist/release/）
  ./scripts/release.sh --no-build       # 用 dist/ 里已有的产物打包（跳过新鲜度校验）
  ./scripts/release.sh --title "体验优化版" --mandatory
选项：--pre / --dry-run / --no-build / -v <版本> / --title <标题> / --channel <通道> / --mandatory
配置：release/release.conf（UPDATE_SITE / APP_ID / UPLOAD_TOKEN / CA_CERT / INSECURE_TLS，
      同名环境变量可覆盖，例如对着本地站点试跑：
      UPDATE_SITE=https://localhost:8443 CA_CERT=certs/server.crt ./scripts/release.sh ...）
EOF
}

# ---------- 参数 ----------

PRE=0
DRY=0
DO_BUILD=1
VERSION=""
TITLE=""
CHANNEL=""
MANDATORY=0

while [ $# -gt 0 ]; do
    case "$1" in
        --pre) PRE=1 ;;
        --dry-run) DRY=1 ;;
        --no-build) DO_BUILD=0 ;;
        -v|--version) VERSION="${2:-}"; shift ;;
        --title) TITLE="${2:-}"; shift ;;
        --channel) CHANNEL="${2:-}"; shift ;;
        --mandatory) MANDATORY=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

# ---------- 配置 ----------

# conf_get 读配置项：release/release.conf 里的 KEY=VALUE，同名环境变量优先。
conf_get() {
    local key="$1" line val="" env_val=""
    if [ -f "$CONF" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
            line="${line%$'\r'}"
            case "$line" in ''|'#'*) continue ;; esac
            case "$line" in "$key="*) val="${line#"$key="}" ;; esac
        done < "$CONF"
    fi
    env_val="$(printenv "$key" 2>/dev/null || true)"
    if [ -n "$env_val" ]; then
        printf '%s' "$env_val"
    else
        printf '%s' "$val"
    fi
}

UPDATE_SITE="$(conf_get UPDATE_SITE)"
APP_ID="$(conf_get APP_ID)"
UPLOAD_TOKEN="$(conf_get UPLOAD_TOKEN)"
CA_CERT="$(conf_get CA_CERT)"
INSECURE_TLS="$(conf_get INSECURE_TLS)"
UPDATE_SITE="${UPDATE_SITE%/}" # 去结尾斜杠

if [ -z "$UPDATE_SITE" ] || [ -z "$APP_ID" ] || [ -z "$UPLOAD_TOKEN" ]; then
    echo "配置不完整：请检查 $CONF 里的 UPDATE_SITE / APP_ID / UPLOAD_TOKEN" >&2
    exit 1
fi

# 站点证书：优先用 CA_CERT 校验（自签名证书以自身为 CA，--cacert 即可），
# 没配就交给 curl 的默认信任链；INSECURE_TLS=1 才跳过校验（调试用）。
CURL_TLS=()
if [ "$INSECURE_TLS" = "1" ]; then
    CURL_TLS+=(-k)
elif [ -n "$CA_CERT" ]; then
    if [ -f "$CA_CERT" ]; then
        CURL_TLS+=(--cacert "$(cygpath -m "$CA_CERT" 2>/dev/null || echo "$CA_CERT")")
    else
        echo "配置的 CA_CERT 不存在（$CA_CERT），本次不校验证书" >&2
        CURL_TLS+=(-k)
    fi
fi

# ---------- 版本 ----------

VERSION_BASE="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' "$ROOT/internal/core/buildinfo.go")"
if [ -z "$VERSION_BASE" ]; then
    echo "无法从 internal/core/buildinfo.go 解析基础版本号" >&2
    exit 1
fi
if [ -z "$VERSION" ]; then
    if [ "$PRE" = 1 ]; then
        VERSION="${VERSION_BASE}-rc"
    else
        VERSION="$VERSION_BASE"
    fi
fi
VERSION="${VERSION#v}"
# 站点对版本目录的要求：^v?\d+(\.\d+)*([-_+][0-9A-Za-z.\-+]*)?$。
# 同时兜住 --version 传入的怪值（版本号会进路径，不能让 ../ 之类混进来）。
if ! grep -Eq '^[0-9]+(\.[0-9]+)*([-_+][0-9A-Za-z.+-]*)?$' <<<"$VERSION"; then
    echo "版本号不合法：$VERSION（形如 1.2.3 / 1.2.3-rc / 2026.10.03）" >&2
    exit 1
fi
case "$VERSION" in
    *-*|*_*) PRERELEASE=true ;;   # 版本号含 - 或 _ → 站点判定为预发布
    *) PRERELEASE=false ;;
esac
[ -n "$TITLE" ] || TITLE="mti-sip-phone ${VERSION}"

echo "发布配置：站点 $UPDATE_SITE，应用 $APP_ID，版本 $VERSION（pre=$PRE dry-run=$DRY）"

# ---------- 编译 ----------

case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) HOST=windows ;;
    Linux) HOST=linux ;;
    *) HOST=other ;;
esac
if [ "$HOST" = other ]; then
    echo "不支持的宿主平台：$(uname -s)（Windows 用 git-bash，Linux 直接跑）" >&2
    exit 1
fi

BUILD_START="$(date +%s)"

if [ "$DO_BUILD" = 1 ]; then
    if [ "$HOST" = windows ]; then
        # Windows 上必须走 build.bat：git-bash 里跑 build.sh 会因 GOOS 默认
        # windows 而产出「名为 linux-* 的 Windows 二进制」。
        BUILD_BAT="$(cygpath -w "$ROOT/build.bat" 2>/dev/null || echo "$ROOT/build.bat")"
        if [ "$PRERELEASE" = true ]; then
            echo "编译（开发版，版本带 -rc）：build.bat"
            cmd //c "$BUILD_BAT"
        else
            echo "编译（正式版，SIP_PHONE_RELEASE=1）：build.bat"
            SIP_PHONE_RELEASE=1 cmd //c "$BUILD_BAT"
        fi
    else
        if [ "$PRERELEASE" = true ]; then
            echo "编译（开发版，版本带 -rc）：build.sh"
            bash "$ROOT/build.sh"
        else
            echo "编译（正式版，SIP_PHONE_RELEASE=1）：build.sh"
            SIP_PHONE_RELEASE=1 bash "$ROOT/build.sh"
        fi
    fi
else
    echo "跳过编译（--no-build）：直接使用 dist/ 里已有的产物"
fi

# ---------- 收集产物 ----------
# 源文件 → 发布文件名 <APP_ID>-<平台>-<架构>[.exe]（固定不含版本号：同一平台的
# 下载地址跨版本不变，用户与脚本收藏的链接不会失效；版本靠版本目录区分，
# 站点文档也允许这种命名——文件名里有平台/架构词即可，何况 release.json 还显式声明了）。
# 只列托盘 GUI 产物：sip-phone-console.exe 是同平台第二个候选，不发布。
ART_SRC=(
    "dist/sip-phone-windows-amd64.exe"
    "dist/sip-phone-windows-386.exe"
    "dist/sip-phone-linux-amd64"
    "dist/sip-phone-linux-arm64"
)
ART_OS=(windows windows linux linux)
ART_ARCH=(x64 x86 x64 arm64)
ART_TITLE=(
    "Windows 64 位（免安装）"
    "Windows 32 位（免安装）"
    "Linux 64 位"
    "Linux ARM64"
)

STAGE="$ROOT/dist/release/$VERSION"
rm -rf "$STAGE"
mkdir -p "$STAGE"

FILES=()   # 发布文件名（相对版本目录）
HASHES=()  # 对应 sha256
SIZES=()   # 对应字节数
MISSING=()

for i in "${!ART_SRC[@]}"; do
    src="$ROOT/${ART_SRC[$i]}"
    if [ ! -f "$src" ]; then
        MISSING+=("${ART_SRC[$i]}")
        continue
    fi
    # 新鲜度：产物必须在本次构建开始之后写出，否则就是上次构建留下的旧二进制
    if [ "$DO_BUILD" = 1 ]; then
        mtime="$(stat -c %Y "$src" 2>/dev/null || echo 0)"
        if [ "$mtime" -lt "$BUILD_START" ]; then
            echo "跳过旧产物（mtime 早于本次构建，未重新生成）：${ART_SRC[$i]}"
            MISSING+=("${ART_SRC[$i]}（旧产物）")
            continue
        fi
    fi
    name="${APP_ID}-${ART_OS[$i]}-${ART_ARCH[$i]}"
    case "$src" in *.exe) name="$name.exe" ;; esac
    cp -f "$src" "$STAGE/$name"
    FILES+=("$name")
    HASHES+=("$(sha256sum "$STAGE/$name" | awk '{print $1}')")
    SIZES+=("$(stat -c %s "$STAGE/$name")")
done

if [ "${#FILES[@]}" -eq 0 ]; then
    echo "dist/ 里没有可发布的产物，发布中止" >&2
    exit 1
fi

echo "收集到 ${#FILES[@]} 个产物："
for i in "${!FILES[@]}"; do
    echo "  ${FILES[$i]}  ($(( SIZES[i] / 1048576 )) MB)"
done
if [ "${#MISSING[@]}" -gt 0 ]; then
    echo "以下产物未包含（缺失或未重新生成）："
    for m in "${MISSING[@]}"; do echo "  $m"; done
    echo "提示：Windows 只能产出 Windows 产物；Linux 产物需在 Linux 机器上跑 build.sh 后再发布。"
fi

# ---------- 版本元数据 ----------

# json_escape：转义 JSON 字符串里的反斜杠与双引号，并把换行压成空格
# （只用于单行的 title / channel；更新说明一律走 CHANGELOG.md，不拼进 JSON）
json_escape() {
    printf '%s' "$1" | tr '\n' ' ' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# 更新说明：优先取版本同名段落；预发布版（如 1.0.5-rc）回落到底版本段（1.0.5，
# 开发期该段落注明「待发布」）。都没有则不写 CHANGELOG.md，站点用 release.json 的 notes。
extract_changelog() {
    [ -f "$ROOT/changelog.md" ] || return 0
    awk -v ver="$1" '
        !found && index($0, "## [" ver "]") == 1 { found = 1; next }
        found && substr($0, 1, 3) == "## " { exit }
        found {
            if (!started && $0 ~ /^[[:space:]]*$/) next
            started = 1
            lines[++n] = $0
            if ($0 ~ /[^[:space:]]/) last = n
        }
        END { for (i = 1; i <= last; i++) print lines[i] }
    ' "$ROOT/changelog.md"
}

NOTES_FROM=""
CHANGELOG_TEXT="$(extract_changelog "$VERSION")"
if [ -n "$CHANGELOG_TEXT" ]; then
    NOTES_FROM="$VERSION"
else
    CHANGELOG_TEXT="$(extract_changelog "$VERSION_BASE")"
    if [ -n "$CHANGELOG_TEXT" ]; then
        NOTES_FROM="$VERSION_BASE"
    fi
fi
if [ -n "$CHANGELOG_TEXT" ]; then
    printf '%s\n' "$CHANGELOG_TEXT" > "$STAGE/CHANGELOG.md"
    echo "更新说明：取自 changelog.md 的 [$NOTES_FROM] 段"
else
    echo "提示：changelog.md 里没有 [$VERSION] / [$VERSION_BASE] 段，发布物不含 CHANGELOG.md"
fi

{
    printf '{\n'
    printf '  "channel": "%s",\n' "$(json_escape "${CHANNEL:-stable}")"
    printf '  "title": "%s",\n' "$(json_escape "$TITLE")"
    printf '  "publishedAt": "%s",\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf '  "prerelease": %s,\n' "$PRERELEASE"
    printf '  "mandatory": %s,\n' "$([ "$MANDATORY" = 1 ] && echo true || echo false)"
    printf '  "artifacts": [\n'
    for i in "${!FILES[@]}"; do
        sep=","
        if [ "$i" -eq $(( ${#FILES[@]} - 1 )) ]; then
            sep=""
        fi
        printf '    {"file": "%s", "name": "%s", "kind": "portable", "os": "%s", "arch": "%s", "sha256": "%s", "size": %s}%s\n' \
            "$(json_escape "${FILES[$i]}")" "$(json_escape "${ART_TITLE[$i]}")" \
            "${ART_OS[$i]}" "${ART_ARCH[$i]}" "${HASHES[$i]}" "${SIZES[$i]}" "$sep"
    done
    printf '  ]\n'
    printf '}\n'
} > "$STAGE/release.json"

# SHA256SUMS：只列产物（站点规范：<哈希>  <文件名>）
: > "$STAGE/SHA256SUMS"
for i in "${!FILES[@]}"; do
    printf '%s  %s\n' "${HASHES[$i]}" "${FILES[$i]}" >> "$STAGE/SHA256SUMS"
done

echo "打包目录：${STAGE#"$ROOT/"}"

# ---------- 打包（tar.gz；git-bash 没有 zip，站点按文件头识别格式，不看扩展名） ----------

TARBALL="$ROOT/dist/release/${APP_ID}-${VERSION}.tar.gz"
rm -f "$TARBALL"
tar -czf "$TARBALL" -C "$STAGE" .
echo "上传包：${TARBALL#"$ROOT/"}（$(stat -c %s "$TARBALL") 字节，$(tar -tzf "$TARBALL" | wc -l) 个条目）"

# ---------- 上传 ----------

if [ "$DRY" = 1 ]; then
    echo "dry-run：不上传（应用信息 / 图标 / 发布包都不写站点）。"
    echo "         产物在 dist/release/，确认无误后去掉 --dry-run 重跑。"
    exit 0
fi

# api_put_file 把文件 PUT 到站点接口：2xx 成功，其余打印服务端错误后返回 1。
# 站点错误码含义见其《REST API》文档（400 字段非法/图片无法识别、401 令牌错、
# 403 站点未开上传、413 超过大小上限、503 归档目录不可写）。
api_put_file() {
    local label="$1" path="$2" url="$3" ctype="$4"
    local arg body code
    arg="$(cygpath -m "$path" 2>/dev/null || echo "$path")"
    body="$(mktemp)"
    code="$(curl -sS --max-time 120 "${CURL_TLS[@]}" -o "$body" -w '%{http_code}' -X PUT \
        -H "Authorization: Bearer $UPLOAD_TOKEN" \
        -H "Content-Type: $ctype" \
        --data-binary "@$arg" "$url")" || {
        echo "$label 写入失败：无法连接 $UPDATE_SITE" >&2
        rm -f "$body"
        return 1
    }
    if [ "$code" = 200 ] || [ "$code" = 201 ]; then
        echo "$label 已写入"
        rm -f "$body"
        return 0
    fi
    echo "$label 写入失败（HTTP $code）：" >&2
    cat "$body" >&2
    echo >&2
    case "$code" in
        400) echo "400：字段非法/超限或含未知字段（图标则是不认识的图片格式）" >&2 ;;
        401) echo "401：令牌缺失或错误（检查 release.conf 的 UPLOAD_TOKEN）" >&2 ;;
        403) echo "403：站点未开启上传功能" >&2 ;;
        413) echo "413：超过大小上限（图标最大 2 MB）" >&2 ;;
        503) echo "503：归档目录不可写（挂载成了只读？）" >&2 ;;
    esac
    rm -f "$body"
    return 1
}

# ---------- 应用级元数据（走站点接口，不直接动归档目录） ----------
# PUT /metadata 是**整份替换**（没写的字段会被清空），所以 release/app.json 要
# 写全；图标单独走 /icon（类型按文件内容判定，文件名由站点决定，别在 app.json
# 里手工写 icon 字段——站点保存图标时会自己把它指过去）。
echo "写入应用级元数据（站点接口）..."
api_put_file "应用信息 app.json" "$ROOT/release/app.json" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/metadata" "application/json" ||
    { echo "应用信息写入失败，发布中止（发布包未上传）" >&2; exit 1; }
api_put_file "应用图标 icon.svg" "$ROOT/release/icon.svg" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/icon" "image/svg+xml" ||
    { echo "应用图标写入失败，发布中止（发布包未上传）" >&2; exit 1; }

TARBALL_ARG="$(cygpath -m "$TARBALL" 2>/dev/null || echo "$TARBALL")"
BODY="$(mktemp)"
# 上传几十 MB，给个进度条：--progress-bar 走 stderr（终端可见），
# stdout 只留 -w 的状态码，仍能被命令替换干净地捕获（所以这里不用 -s/-sS）。
echo "上传发布包（$(stat -c %s "$TARBALL") 字节，$(basename "$TARBALL")）…"
CODE="$(curl -S --progress-bar --max-time 600 "${CURL_TLS[@]}" -o "$BODY" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $UPLOAD_TOKEN" \
    -F "file=@$TARBALL_ARG" \
    -F "version=$VERSION" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/upload")" || {
    echo "上传失败：无法连接 $UPDATE_SITE" >&2
    rm -f "$BODY"
    exit 1
}

if [ "$CODE" = 201 ]; then
    echo "上传成功（201）："
    cat "$BODY"
    echo
    if grep -q '"replaced": *true' "$BODY"; then
        echo "注意：站点上已存在同名版本，已整体替换。"
    fi
else
    echo "上传失败（HTTP $CODE）：" >&2
    cat "$BODY" >&2
    echo >&2
    case "$CODE" in
        400) echo "400：应用 ID 非法、版本号无法确定、压缩包损坏或不含可安装文件" >&2 ;;
        401) echo "401：令牌缺失或错误（检查 release.conf 的 UPLOAD_TOKEN）" >&2 ;;
        403) echo "403：站点关闭了上传（UPLOAD_ENV 里 UPLOAD_ENABLED=false）" >&2 ;;
        413) echo "413：超过站点 MAX_UPLOAD 限制" >&2 ;;
        503) echo "503：归档目录不可写（挂载成了只读？）" >&2 ;;
    esac
    rm -f "$BODY"
    exit 1
fi
rm -f "$BODY"

# ---------- 发布后核对 ----------

PROBE="$UPDATE_SITE/api/v1/apps/$APP_ID/check?version=$VERSION_BASE&os=windows&arch=amd64&prerelease=true"
if CHECK="$(curl -sS --max-time 30 "${CURL_TLS[@]}" "$PROBE" 2>/dev/null)"; then
    if grep -q '"updateAvailable": *true' <<<"$CHECK"; then
        echo "站点核对：已能看到新版本（低版本客户端会收到更新提示）"
    else
        echo "站点核对：check 尚未反映新版本（扫描周期默认 15 秒，稍后可重试）"
    fi
fi

echo
echo "发布完成：$VERSION"
echo "  Windows 下载页：$UPDATE_SITE/a/$APP_ID/$VERSION"
echo "  API：          $UPDATE_SITE/api/v1/apps/$APP_ID/releases/$VERSION"
echo "  最新版查询：    $UPDATE_SITE/api/v1/apps/$APP_ID/check?version=$VERSION_BASE&os=windows&arch=amd64&prerelease=true"
exit 0
```

### 附录 B：版本比较 `internal/updater/version.go`（完整）

```go
package updater

import (
	"strconv"
	"strings"
)

// compareVersions 比较版本号：a > b 返回 (1, true)，a < b 返回 (-1, true)，
// 相等返回 (0, true)；任一侧无法解析时返回 (0, false)（视为不可比）。
//
// 规则（与更新站点的语义化版本一致）：
//   - 主版本按 "." 分段做数字比较（1.0.10 > 1.0.9），缺段按 0，允许 "v" 前缀；
//   - "-" 或 "_" 之后是预发布标识，**预发布低于同号正式版**（1.0.5-rc < 1.0.5）
//     ——开发构建的版本号带 -rc，正式版发布时数字相同也必须能通知更新；
//   - 预发布标识逐段比较：数字段按数值、数字段低于字母数字段、段数少者低
//     （1.0.5-rc < 1.0.5-rc.1）；
//   - "+" 之后是构建元数据，不参与比较（1.0+meta == 1.0）。
//
// 不可比时调用方应放行——版本裁决权在前端，软电话只在可比时拒绝降级。
func compareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	if c := compareNumericParts(av.parts, bv.parts); c != 0 {
		return c, true
	}
	return comparePrerelease(av.pre, bv.pre), true
}

// parsedVersion 解析后的版本：数字段 + 预发布标识（正式版为空）。
type parsedVersion struct {
	parts []int
	pre   []string
}

// parseVersion 把 "1.2.3" / "v1.2.3-rc.1" 解析为数字段与预发布标识。
func parseVersion(v string) (parsedVersion, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return parsedVersion{}, false
	}
	// 构建元数据不参与比较
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	// "-" 与 "_" 都引入预发布标识（与站点一致，如 1.2.3-rc.1 / 1.2.3_rc1）
	var pre []string
	if i := strings.IndexAny(v, "-_"); i >= 0 {
		pre = strings.FieldsFunc(v[i+1:], func(r rune) bool {
			return r == '.' || r == '-' || r == '_'
		})
		v = v[:i]
	}
	if v == "" {
		return parsedVersion{}, false
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return parsedVersion{}, false
		}
		parts = append(parts, n)
	}
	return parsedVersion{parts: parts, pre: pre}, true
}

// compareNumericParts 比较数字段（缺段按 0）。
func compareNumericParts(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrerelease 比较预发布标识：任一侧为空时正式版更高。
func comparePrerelease(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // 正式版 > 同号预发布版
	case len(b) == 0:
		return -1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // 数字段低于字母数字段（语义化版本规则）
		case yerr == nil:
			return 1
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1 // 前缀相同时段数少者低（1.0.5-rc < 1.0.5-rc.1）
	case len(a) > len(b):
		return 1
	}
	return 0
}
```

### 附录 C：更新状态机与站点查询 `internal/updater/updater.go`（完整）

> 查询（REQ-UPD-017）在同包的 `internal/updater/checker.go`，接在下面第二段。

```go
// Package updater 实现自动更新：**软电话自己**按配置的周期查询更新站点
// （见 checker.go，REQ-UPD-017），查到更高版本后经界面提示用户，由用户确认后
// 下载（可选 sha256 与可执行文件魔数校验）、替换当前可执行文件并重启；
// 业务前端仍可经 WS 强制推送某个版本（`system.notifyUpdate` + force）。
// 版本比较在可比时拒绝降级：预发布低于同号正式版（开发构建版本号带 -rc，
// 正式版发布时数字相同也必须能通知更新，规则见 version.go）。
//
// 状态变化经 core.Bus 发布 "system.update" 事件（UI 渲染、WS 转发给网页）。
// 锁序纪律：本包持 u.mu 时不得调用 Options.IsBusy（它会取 core.App 的锁）；
// core.App.State() 也在进 a.mu 前先取本包快照——两把锁不得互相等待。
package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// Options 构造参数（由 cmd/sip-phone 注入，测试可替换）。
type Options struct {
	Bus        *core.Bus    // 事件发布（system.update）；可为 nil
	ExePath    string       // 当前可执行文件路径；空则取 os.Executable()
	Restart    func() error // 替换成功后调用：请求进程重启（由 cmd 实现）
	IsBusy     func() bool  // 是否有进行中的通话（nil = 不等待）
	HTTPClient *http.Client // 测试注入；nil 用默认客户端
	Timeout    time.Duration
}

// Updater 自动更新状态机。
type Updater struct {
	opts    Options
	exePath string

	mu          sync.Mutex
	phase       string
	info        core.UpdateInfo
	host        string
	downloaded  int64
	total       int64
	progress    float64
	waitingCall bool
	applying    bool
	errMsg      string

	cancel   context.CancelFunc // 取消进行中的下载
	stopWait chan struct{}      // 通知"等待通话结束"的轮询退出

	waitInterval time.Duration // 等待通话结束的轮询间隔（测试可缩短）

	lastPublish  time.Time
	lastProgress float64

	// ---- 更新站点查询（checker.go，REQ-UPD-017）----
	// 单独一把锁：轮询 goroutine 读写站点配置与去重键，与上面的下载状态机无关
	//（两者只在 Notify 处交汇，Notify 自己取 mu）。
	checkMu      sync.Mutex
	site         core.SiteConfigSnapshot
	lastNotified string        // 已告知过的「版本|sha256」（同一版本不反复弹窗）
	checkOnce    sync.Once     // Start 只起一个轮询 goroutine
	checkKick    chan struct{} // 催一次检查（容量 1，非阻塞投递）
}

// New 创建 Updater。
func New(opts Options) *Updater {
	exe := opts.ExePath
	if exe == "" {
		if p, err := os.Executable(); err == nil {
			exe = p
		}
	}
	return &Updater{opts: opts, exePath: exe, phase: core.UpdateIdle, waitInterval: 2 * time.Second}
}

// Notify 接收前端告知的新版本：校验参数与版本后进入 available 状态。
// 返回 core.ErrUpdateNotNewer 表示版本不高于当前（前端应视为正常结果）。
func (u *Updater) Notify(info core.UpdateInfo) error {
	info.Version = strings.TrimSpace(info.Version)
	info.URL = strings.TrimSpace(info.URL)
	info.SHA256 = strings.ToLower(strings.TrimSpace(info.SHA256))
	info.OS = strings.TrimSpace(info.OS)
	info.Arch = strings.TrimSpace(info.Arch)

	if info.Version == "" || info.URL == "" {
		return fmt.Errorf("%w：版本号与下载地址不能为空", core.ErrUpdateInvalid)
	}
	parsed, err := url.Parse(info.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w：下载地址无效（仅支持 http/https）", core.ErrUpdateInvalid)
	}
	if info.OS != "" && info.OS != runtime.GOOS {
		return fmt.Errorf("%w：目标平台 %s 与当前 %s 不符", core.ErrUpdateInvalid, info.OS, runtime.GOOS)
	}
	if info.Arch != "" && info.Arch != runtime.GOARCH {
		return fmt.Errorf("%w：目标架构 %s 与当前 %s 不符", core.ErrUpdateInvalid, info.Arch, runtime.GOARCH)
	}
	// 强制推送（联调/回滚用）跳过版本高低检查；平台一致性等其余校验照旧。
	if !info.Force {
		if cmp, ok := compareVersions(info.Version, core.Version); ok && cmp <= 0 {
			return fmt.Errorf("%w：当前 %s，收到 %s", core.ErrUpdateNotNewer, core.Version, info.Version)
		}
	}

	u.mu.Lock()
	switch u.phase {
	case core.UpdateDownloading, core.UpdateReady:
		if u.info.Version == info.Version {
			u.mu.Unlock()
			logx.Logger().Debug("重复的更新通知，已忽略", "version", info.Version)
			return nil
		}
		u.mu.Unlock()
		return core.ErrUpdateInProgress
	}
	u.phase = core.UpdateAvailable
	u.info = info
	u.host = parsed.Hostname()
	u.downloaded, u.total, u.progress = 0, 0, 0
	u.progress, u.lastProgress = 0, 0
	u.waitingCall = false
	u.errMsg = ""
	st := u.statusLocked()
	u.mu.Unlock()

	logx.Logger().Info("收到新版本通知", "version", info.Version, "host", st.Host,
		"current", core.Version, "强制推送", info.Force)
	u.publish(st)
	return nil
}

// Download 开始下载（异步，立即返回）。
func (u *Updater) Download() error {
	u.mu.Lock()
	if u.phase == core.UpdateDownloading {
		u.mu.Unlock()
		return core.ErrUpdateInProgress
	}
	if u.phase != core.UpdateAvailable && u.phase != core.UpdateFailed {
		u.mu.Unlock()
		return fmt.Errorf("当前没有可下载的更新")
	}
	info := u.info
	timeout := u.opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	u.cancel = cancel
	u.phase = core.UpdateDownloading
	u.downloaded, u.total, u.progress = 0, 0, 0
	u.lastProgress, u.lastPublish = 0, time.Time{}
	u.waitingCall = false
	u.errMsg = ""
	st := u.statusLocked()
	u.mu.Unlock()

	logx.Logger().Info("开始下载更新", "version", info.Version, "url", info.URL)
	u.publish(st)
	go u.runDownload(ctx, info)
	return nil
}

// Cancel 取消进行中的下载（回到可重试状态），或撤销等待中的重启。
func (u *Updater) Cancel() {
	u.mu.Lock()
	cancel := u.cancel
	wasWaiting := u.waitingCall
	u.waitingCall = false
	if u.stopWait != nil {
		close(u.stopWait)
		u.stopWait = nil
	}
	u.mu.Unlock()

	if cancel != nil {
		cancel() // 下载 goroutine 会自行清理并回到 available
	}
	if wasWaiting {
		logx.Logger().Info("用户取消等待重启，更新保持就绪")
		u.publishStatus()
	}
}

// Apply 替换可执行文件并重启；有通话进行中时挂起等待，通话结束后自动执行。
func (u *Updater) Apply() error {
	u.mu.Lock()
	if u.phase != core.UpdateReady {
		u.mu.Unlock()
		return fmt.Errorf("更新尚未下载完成")
	}
	u.mu.Unlock()

	// IsBusy 取 core.App 的锁：必须在释放 u.mu 之后调用（锁序纪律）
	if u.opts.IsBusy != nil && u.opts.IsBusy() {
		u.mu.Lock()
		if u.waitingCall || u.applying {
			u.mu.Unlock()
			return nil
		}
		u.waitingCall = true
		stop := make(chan struct{})
		u.stopWait = stop
		st := u.statusLocked()
		u.mu.Unlock()

		logx.Logger().Info("下载完成，等待通话结束后重启安装")
		u.publish(st)
		go u.waitIdleThenApply(stop)
		return nil
	}
	return u.applyNow()
}

// waitIdleThenApply 通话结束后自动应用更新（每 2 秒轮询一次，Cancel 可中止）。
func (u *Updater) waitIdleThenApply(stop chan struct{}) {
	interval := u.waitInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if u.opts.IsBusy != nil && u.opts.IsBusy() {
				continue
			}
			u.mu.Lock()
			if u.phase != core.UpdateReady || !u.waitingCall {
				u.mu.Unlock()
				return
			}
			u.waitingCall = false
			u.stopWait = nil
			u.mu.Unlock()
			u.publishStatus()
			if err := u.applyNow(); err != nil {
				logx.Logger().Error("自动重启安装失败", "err", err)
			}
			return
		}
	}
}

// applyNow 执行替换与重启（调用方须确保已通过 IsBusy 检查或承担后果）。
func (u *Updater) applyNow() error {
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return nil
	}
	u.applying = true
	info := u.info
	u.mu.Unlock()

	if err := u.replaceExe(); err != nil {
		u.fail(friendlyError(err))
		return err
	}
	logx.Logger().Info("更新已应用，正在重启", "version", info.Version)
	if u.opts.Restart != nil {
		if err := u.opts.Restart(); err != nil {
			u.fail("重启失败：" + err.Error())
			return err
		}
	}
	return nil
}

// fail 进入失败状态并推送事件。
func (u *Updater) fail(msg string) {
	u.mu.Lock()
	u.applying = false
	u.phase = core.UpdateFailed
	u.errMsg = msg
	u.waitingCall = false
	st := u.statusLocked()
	u.mu.Unlock()
	logx.Logger().Warn("更新失败", "err", msg)
	u.publish(st)
}

// Status 返回当前更新状态快照。
func (u *Updater) Status() core.UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked()
}

// statusLocked 组装状态快照（须持 u.mu）。
func (u *Updater) statusLocked() core.UpdateStatus {
	return core.UpdateStatus{
		Phase:       u.phase,
		Info:        u.info,
		Host:        u.host,
		Downloaded:  u.downloaded,
		Total:       u.total,
		Progress:    u.progress,
		WaitingCall: u.waitingCall,
		Error:       u.errMsg,
	}
}

// publishStatus 取快照并发布事件（不持锁调用 publish 之前的状态读取）。
func (u *Updater) publishStatus() {
	u.mu.Lock()
	st := u.statusLocked()
	u.mu.Unlock()
	u.publish(st)
}

// publish 发布 system.update 事件（总线事件会自动转发给所有 WS 客户端）。
func (u *Updater) publish(st core.UpdateStatus) {
	if u.opts.Bus != nil {
		u.opts.Bus.Publish("system.update", st)
	}
}
```

### 附录 C2：更新站点查询 `internal/updater/checker.go`（完整）

```go
package updater

// 更新站点查询（REQ-UPD-017）：软电话自己按周期问站点"有没有新版本"。
//
// 这段逻辑原先在网页端 SDK 里（`checkUpdate` / `_checkURL`），搬到这里之后
// **页面没打开也会检查**。查询结果不直接下载——照旧走 Notify → system.update
// 事件 → 界面右下角提示窗 → 用户点「更新」才下载替换重启（REQ-UPD-009）。
//
// 与 SDK 时期一致的几条口径：
//   - 判定权在站点：只看 `updateAvailable`，软电话这边不再自己比一遍版本
//     （Notify 里那次比较是防降级的兜底）；
//   - **不带 channel**：用站点的应用默认通道；
//   - 查询参数**不要带 os/arch 去 Notify**：站点回的是规范化值（x64/x86），
//     而 Notify 按 runtime.GOARCH 严格比对（amd64/386），传了会被判平台不符；
//   - 同一版本 + 同一 sha256 只告知一次（滚动的周期不会反复弹窗）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

const (
	// checkTimeout 单次查询的超时（与旧 SDK 的 10 秒一致）。
	checkTimeout = 10 * time.Second
	// checkBodyLimit 响应体读取上限（防站点异常返回超大内容）。
	checkBodyLimit = 1 << 20
)

// SetUpdateConfig 下发更新站点配置：空 URL = 不检查（停表）。
// 配置变化时立即重排下一次检查（改完就查一次，不用等一个周期）。
func (u *Updater) SetUpdateConfig(c core.SiteConfigSnapshot) {
	u.checkMu.Lock()
	changed := u.site != c
	u.site = c
	u.lastNotified = "" // 配置（站点/AppID/预览版）变了，允许重新告知同版本
	u.checkMu.Unlock()
	if !changed {
		return
	}
	logx.Logger().Info("更新检查配置已更新",
		"站点", c.URL, "应用", c.AppID, "周期", c.Interval.String(), "接受预览版", c.Prerelease)
	u.kickCheck()
}

// Start 启动轮询（幂等，可重复调用）。ctx 结束时退出。
func (u *Updater) Start(ctx context.Context) {
	u.checkOnce.Do(func() {
		u.checkKick = make(chan struct{}, 1)
		go u.checkLoop(ctx)
	})
}

// kickCheck 非阻塞地催一次检查（通道容量 1 + select default：任何一方都不 close，
// 与 netwatch 的 kick 同一套写法——阻塞发送会把调用方卡死）。
func (u *Updater) kickCheck() {
	if u.checkKick == nil {
		return
	}
	select {
	case u.checkKick <- struct{}{}:
	default:
	}
}

// checkLoop 循环：配置了就按周期查；配置变更/手动检查通过 kick 立即重排。
func (u *Updater) checkLoop(ctx context.Context) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	first := true // 首次立即查一次（启动时配了站点就不用干等一个周期）
	for {
		u.checkMu.Lock()
		site := u.site
		u.checkMu.Unlock()

		var wait time.Duration
		switch {
		case site.URL == "":
			wait = 24 * time.Hour // 没配站点：不查（哨兵值，等 kick 唤醒）
		case first:
			wait = 0 // 立即
		default:
			wait = site.Interval
		}
		first = false
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Stop()
			timer.Reset(wait)
		}

		select {
		case <-ctx.Done():
			return
		case <-u.checkKick:
			// 配置变了/手动检查：立刻查一次（下面这轮循环计时从头开始）
		case <-timer.C:
		}

		if ctx.Err() != nil {
			return
		}
		if _, err := u.CheckNow(ctx); err != nil {
			// CheckNow 内部已记日志与结果，这里不再重复
			continue
		}
	}
}

// CheckNow 立即查询一次站点并（必要时）告知界面。ctx 为 nil 时用后台 context。
func (u *Updater) CheckNow(ctx context.Context) (core.CheckResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u.checkMu.Lock()
	site := u.site
	u.checkMu.Unlock()

	if site.URL == "" {
		return core.CheckResult{Skipped: true}, nil
	}

	res, err := u.querySite(ctx, site)
	if err != nil {
		logx.Logger().Warn("检查更新失败", "站点", site.URL, "err", err)
		res.Err = err
		return res, err
	}

	// 已是最新 / 无本平台产物：不打扰用户
	if !res.Notified {
		logx.Logger().Debug("检查更新完成", "站点", site.URL, "最新", res.LatestVersion, "结果", res.Text())
	}
	return res, nil
}

// checkPayload 站点 check 接口的响应（只取用得到的字段）。
type checkPayload struct {
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Notes           string `json:"notes"`
	Download        *struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"download"`
}

// querySite 发一次查询；命中更高版本时告知界面（updater 进入 available）。
func (u *Updater) querySite(ctx context.Context, site core.SiteConfigSnapshot) (core.CheckResult, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.checkURL(site), nil)
	if err != nil {
		return core.CheckResult{}, err
	}
	req.Header.Set("User-Agent", "mti-sip-phone/"+core.Version)

	resp, err := u.client().Do(req)
	if err != nil {
		return core.CheckResult{}, errors.New(friendlyError(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, checkBodyLimit))
	if err != nil {
		return core.CheckResult{}, errors.New(friendlyError(err))
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return core.CheckResult{}, fmt.Errorf("更新站点没有该应用（HTTP 404）")
		}
		return core.CheckResult{}, fmt.Errorf("更新站点返回 HTTP %d", resp.StatusCode)
	}

	var p checkPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return core.CheckResult{}, fmt.Errorf("更新站点响应解析失败: %w", err)
	}
	res := core.CheckResult{LatestVersion: p.LatestVersion}
	if !p.UpdateAvailable {
		res.UpToDate = true
		res.Reason = "up_to_date"
		return res, nil
	}
	if p.Download == nil || strings.TrimSpace(p.Download.URL) == "" {
		// 该平台暂无产物：站点判"有更新"但没有可下载的东西，不算错误
		res.UpToDate = true
		res.Reason = "no_download"
		return res, nil
	}

	key := p.LatestVersion + "|" + strings.ToLower(p.Download.SHA256)
	u.checkMu.Lock()
	dup := key == u.lastNotified
	u.checkMu.Unlock()
	if dup {
		res.Reason = "already_notified"
		return res, nil
	}

	err = u.Notify(core.UpdateInfo{
		Version: p.LatestVersion,
		URL:     p.Download.URL,
		SHA256:  p.Download.SHA256,
		Notes:   p.Notes,
		// 刻意不传 OS/Arch：站点回的是 x64/x86 这类规范化值，而 Notify 按
		// runtime.GOARCH 严格比对（amd64/386），传了会被判平台不符而拒绝。
	})
	switch {
	case err == nil:
		u.checkMu.Lock()
		u.lastNotified = key
		u.checkMu.Unlock()
		res.Notified = true
		logx.Logger().Info("检查到新版本", "版本", p.LatestVersion, "站点", site.URL)
	case isNotNewer(err):
		// 站点说可更新、本地版本比较却更高或相同（例如刚装好）——不算错误
		res.Reason = "not_newer"
	case err != nil:
		res.Reason = "notify_failed"
		res.Err = err
		return res, err
	}
	return res, nil
}

// isNotNewer 是否为"版本不高于当前"（站点说可更新但本地比较不成立，属正常结果）。
func isNotNewer(err error) bool {
	return errors.Is(err, core.ErrUpdateNotNewer)
}

// checkURL 拼查询地址：`{站点}/api/v1/apps/{应用}/check?version=&os=&arch=[&prerelease=true]`。
// **不带 channel**（用站点里应用的默认通道）。
func (u *Updater) checkURL(site core.SiteConfigSnapshot) string {
	q := url.Values{}
	q.Set("version", core.Version)
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	if site.Prerelease {
		q.Set("prerelease", "true")
	}
	return strings.TrimRight(site.URL, "/") + "/api/v1/apps/" +
		url.PathEscape(site.AppID) + "/check?" + q.Encode()
}
```

### 附录 D：下载与校验 `internal/updater/download.go`（完整）

```go
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// maxDownloadSize 下载体积上限：防御误填的超大文件（正常产物 ~30MB）。
const maxDownloadSize = 256 << 20

// runDownload 下载协程：完成后置 ready 并自动进入重启流程（有通话则等待）。
func (u *Updater) runDownload(ctx context.Context, info core.UpdateInfo) {
	err := u.download(ctx, info)

	u.mu.Lock()
	if u.cancel != nil {
		u.cancel = nil
	}
	switch {
	case err == nil:
		u.phase = core.UpdateReady
		u.progress = 100
		u.errMsg = ""
	case errors.Is(err, context.Canceled):
		// 用户取消：回到可重试状态，不算失败
		u.phase = core.UpdateAvailable
		u.downloaded, u.progress = 0, 0
		u.errMsg = ""
	default:
		u.phase = core.UpdateFailed
		u.errMsg = friendlyError(err)
	}
	st := u.statusLocked()
	u.mu.Unlock()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			logx.Logger().Info("更新下载已取消")
		} else {
			logx.Logger().Warn("更新下载失败", "err", err)
		}
		u.publish(st)
		return
	}
	logx.Logger().Info("更新下载完成", "version", info.Version, "bytes", st.Total)
	u.publish(st)

	// 用户点击「更新」即授权了"下载→重启"整条链路：下载完成后自动重启，
	// 通话中则由 Apply 挂起等待（并发/重复调用由 Apply 自身去重）
	if err := u.Apply(); err != nil {
		logx.Logger().Error("重启安装失败", "err", err)
	}
}

// download 下载并校验更新文件：写 <exe>.new.tmp → 校验 → 改名 <exe>.new。
func (u *Updater) download(ctx context.Context, info core.UpdateInfo) (err error) {
	dir := filepath.Dir(u.exePath)
	// 前置可写预检：程序目录不可读时立即失败，避免下载到一半才报错
	if err = checkWritable(dir); err != nil {
		return err
	}

	tmp := u.exePath + newSuffix + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return fmt.Errorf("下载地址无效: %w", err)
	}
	req.Header.Set("User-Agent", "mti-sip-phone/"+core.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务器返回 %s", resp.Status)
	}
	if resp.ContentLength > maxDownloadSize {
		return fmt.Errorf("文件大小 %d MB 超过上限", resp.ContentLength>>20)
	}
	u.mu.Lock()
	u.total = resp.ContentLength
	u.mu.Unlock()

	hasher := sha256.New()
	buf := make([]byte, 64<<10)
	var downloaded int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxDownloadSize {
				return fmt.Errorf("文件超过 %d MB 上限", maxDownloadSize>>20)
			}
			hasher.Write(buf[:n])
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入文件失败（磁盘空间不足？）: %w", werr)
			}
			u.setProgress(downloaded)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 注意返回 rerr 而非命名返回值 err——err 此时为 nil，
			// 写错会把"网络中断"当成下载成功（单测已覆盖）
			return rerr
		}
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if downloaded == 0 {
		return errors.New("下载内容为空")
	}
	if info.SHA256 != "" {
		sum := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(sum, info.SHA256) {
			return errors.New("下载内容校验失败（sha256 不匹配），请重试")
		}
	}
	if err = checkExecutableMagic(tmp); err != nil {
		return err
	}
	// 同目录改名：与 exe 同卷，rename 为原子操作
	if err = os.Rename(tmp, u.exePath+newSuffix); err != nil {
		return fmt.Errorf("保存更新文件失败: %w", err)
	}
	return nil
}

// client 返回 HTTP 客户端（总超时由 ctx 控制，故客户端不设 Timeout）。
func (u *Updater) client() *http.Client {
	if u.opts.HTTPClient != nil {
		return u.opts.HTTPClient
	}
	return &http.Client{}
}

// setProgress 更新下载进度并节流发布事件（≥200ms 或 ≥1% 才推一次）。
func (u *Updater) setProgress(downloaded int64) {
	u.mu.Lock()
	u.downloaded = downloaded
	if u.total > 0 {
		u.progress = float64(downloaded) * 100 / float64(u.total)
	}
	now := time.Now()
	publish := now.Sub(u.lastPublish) >= 200*time.Millisecond ||
		(u.total > 0 && u.progress-u.lastProgress >= 1)
	if publish {
		u.lastPublish = now
		u.lastProgress = u.progress
	}
	st := u.statusLocked()
	u.mu.Unlock()
	if publish {
		u.publish(st)
	}
}

// checkWritable 探测目录是否可写（建一个临时文件再删）。
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".mti-update-probe-*")
	if err != nil {
		return fmt.Errorf("程序目录不可写，请以管理员身份运行或手动更新")
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// checkExecutableMagic 校验下载内容是否为当前平台的可执行文件
// （防止把 HTML 错误页/压缩包当成程序替换上去）。
func checkExecutableMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("读取下载文件失败: %w", err)
	}
	defer f.Close()

	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	switch runtime.GOOS {
	case "windows":
		if n < 2 || head[0] != 'M' || head[1] != 'Z' {
			return errors.New("下载内容不是有效的程序文件")
		}
		// 进一步确认 PE 签名（e_lfanew 位于 0x3C）
		if n < 0x40 {
			return errors.New("下载内容不是有效的程序文件")
		}
		off := binary.LittleEndian.Uint32(head[0x3C:0x40])
		fi, serr := f.Stat()
		if serr != nil || int64(off)+4 > fi.Size() {
			return errors.New("下载内容不是有效的程序文件")
		}
		if _, serr := f.Seek(int64(off), io.SeekStart); serr != nil {
			return fmt.Errorf("读取下载文件失败: %w", serr)
		}
		sig := make([]byte, 4)
		if _, serr := io.ReadFull(f, sig); serr != nil ||
			sig[0] != 'P' || sig[1] != 'E' || sig[2] != 0 || sig[3] != 0 {
			return errors.New("下载内容不是有效的程序文件")
		}
	case "linux":
		if n < 4 || head[0] != 0x7f || head[1] != 'E' || head[2] != 'L' || head[3] != 'F' {
			return errors.New("下载内容不是有效的程序文件")
		}
	}
	return nil
}

// friendlyError 把底层错误转成面向用户的中文提示（尽量简短，
// 详细原因由日志记录；界面空间有限，不放 Go 原始错误文本）。
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "下载超时，请重试"
	}
	var uerr interface{ Timeout() bool }
	if errors.As(err, &uerr) && uerr.Timeout() {
		return "连接更新服务器超时，请重试"
	}
	// 网络层错误（连接被拒绝/不可达/DNS 等）：统一给简短提示
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return "无法连接更新服务器，请重试"
	}
	msg := err.Error()
	// 明确的下载/校验类错误原样展示（本身已是中文且简短）
	switch {
	case strings.Contains(msg, "下载内容不是有效"),
		strings.Contains(msg, "校验失败"),
		strings.Contains(msg, "程序目录不可写"),
		strings.Contains(msg, "服务器返回"):
		return msg
	}
	if len(msg) > 40 {
		msg = msg[:40] + "…"
	}
	return "下载失败：" + msg
}
```

### 附录 E：替换自身与回滚 `internal/updater/replace.go`（完整）

```go
package updater

import (
	"fmt"
	"os"
	"time"

	"mti-sip-phone/internal/logx"
)

// 更新文件后缀：<exe>.new.tmp 下载中 → <exe>.new 就绪 → <exe>.old 旧文件备份。
const (
	newSuffix = ".new"
	oldSuffix = ".old"
	tmpSuffix = ".tmp"
)

// replaceExe 用 <exe>.new 替换当前可执行文件：
// 现文件改名为 .old（Windows 上运行中的镜像允许改名，不能覆盖）→ 新文件
// 就位；第二步失败回滚，避免留下"没有可执行文件"的状态。
func (u *Updater) replaceExe() error {
	if u.exePath == "" {
		return fmt.Errorf("无法确定当前可执行文件路径")
	}
	newPath := u.exePath + newSuffix
	oldPath := u.exePath + oldSuffix

	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("更新文件不存在，请重新下载: %w", err)
	}
	// 上次运行的残留备份（正常启动时已由 Cleanup 清理）
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法删除旧的备份文件（%s）: %w", oldPath, err)
	}

	if err := renameWithRetry(u.exePath, oldPath); err != nil {
		return fmt.Errorf("无法备份当前程序（可能被杀毒软件占用，请关闭后重试）: %w", err)
	}
	if err := renameWithRetry(newPath, u.exePath); err != nil {
		if rbErr := renameWithRetry(oldPath, u.exePath); rbErr != nil {
			logx.Logger().Error("更新回滚失败", "err", rbErr, "backup", oldPath)
		}
		return fmt.Errorf("替换程序文件失败: %w", err)
	}
	return nil
}

// renameFile 重命名实现，测试可替换以注入失败场景。
var renameFile = os.Rename

// renameWithRetry 重命名重试：杀毒软件/索引器可能短暂占用文件。
func renameWithRetry(oldPath, newPath string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = renameFile(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// Cleanup 清理更新残留文件（启动时调用，须在取得单实例锁之后，
// 避免删到另一个实例正在使用的文件）。
func (u *Updater) Cleanup() {
	if u.exePath == "" {
		return
	}
	for _, suffix := range []string{oldSuffix, newSuffix, newSuffix + tmpSuffix} {
		p := u.exePath + suffix
		if err := os.Remove(p); err == nil {
			logx.Logger().Info("已清理更新残留文件", "path", p)
		} else if !os.IsNotExist(err) {
			logx.Logger().Warn("清理更新残留文件失败", "path", p, "err", err)
		}
	}
}
```

### 附录 F：更新相关类型 `internal/core/update.go`（完整）

```go
package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"mti-sip-phone/internal/config"
)

// UpdateInfo 前端告知的新版本信息（WS system.notifyUpdate 的参数）。
type UpdateInfo struct {
	Version string `json:"version"`          // 新版本号，如 1.1.0
	URL     string `json:"url"`              // 下载地址（仅 http/https）
	SHA256  string `json:"sha256,omitempty"` // 可选：sha256 校验值（十六进制，大小写不敏感）
	Notes   string `json:"notes,omitempty"`  // 可选：更新说明
	OS      string `json:"os,omitempty"`     // 可选：前端认定的目标平台，提供时须与自身一致
	Arch    string `json:"arch,omitempty"`   // 可选：前端认定的目标架构，提供时须与自身一致
	// Force 强制推送：跳过"版本必须更高"的检查（联调/回滚时按指定版本强推）。
	// 只由调用方显式设置——SDK 的定时查询始终走版本比较，不会带这个字段。
	Force bool `json:"force,omitempty"`
}

// 更新状态机阶段。
const (
	UpdateIdle        = "idle"        // 无可用更新
	UpdateAvailable   = "available"   // 已收到更新通知，等待用户确认下载
	UpdateDownloading = "downloading" // 正在下载
	UpdateReady       = "ready"       // 下载并校验完成，等待替换重启
	UpdateFailed      = "failed"      // 下载/替换失败（可重试）
)

// UpdateStatus 更新状态快照（供 UI 渲染与 WS 事件推送）。
type UpdateStatus struct {
	Phase       string     `json:"phase"`
	Info        UpdateInfo `json:"info"`
	Host        string     `json:"host,omitempty"`       // 下载地址主机名（界面展示来源）
	Downloaded  int64      `json:"downloaded,omitempty"` // 已下载字节数
	Total       int64      `json:"total,omitempty"`      // 总字节数（0 = 未知）
	Progress    float64    `json:"progress,omitempty"`   // 下载进度 0-100
	WaitingCall bool       `json:"waitingCall,omitempty"` // 已就绪，等通话结束后自动重启
	Error       string     `json:"error,omitempty"`      // 面向用户的中文失败原因
}

// 更新相关错误，供接口层映射为错误码。
var (
	ErrUpdateUnavailable = errors.New("更新能力不可用")
	ErrUpdateNotNewer    = errors.New("版本不高于当前版本")
	ErrUpdateInProgress  = errors.New("更新正在进行中")
	ErrUpdateInvalid     = errors.New("更新参数无效")
)

// Updater 自动更新能力抽象（internal/updater 实现，模式同 Telephony：
// 接口定义在 core，实现包 import core；nil 表示未接入）。
type Updater interface {
	// Notify 接收前端告知的新版本信息：校验后进入 available 状态。
	Notify(info UpdateInfo) error
	// Download 开始下载已告知的新版本（异步，立即返回）。
	Download() error
	// Cancel 取消进行中的下载，或撤销等待中的重启。
	Cancel()
	// Apply 替换当前可执行文件并重启；通话中则等通话结束后执行。
	Apply() error
	// Status 返回当前更新状态快照。
	Status() UpdateStatus
	// SetUpdateConfig 下发更新站点配置（空 URL = 不检查），REQ-UPD-017。
	SetUpdateConfig(site SiteConfigSnapshot)
	// CheckNow 立即查询一次更新站点（界面「立即检查」用）。
	CheckNow(ctx context.Context) (CheckResult, error)
}

// SetUpdater 注入自动更新能力（启动前调用，nil 表示未接入）。
func (a *App) SetUpdater(upd Updater) { a.upd = upd }

// Updater 返回自动更新能力实现（可能为 nil）。
func (a *App) Updater() Updater { return a.upd }

// NotifyUpdate 接收前端告知的新版本（WS system.notifyUpdate）。
func (a *App) NotifyUpdate(info UpdateInfo) error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Notify(info)
}

// StartUpdate 开始下载（由用户在软电话界面上点击触发，绝不自动下载）。
func (a *App) StartUpdate() error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Download()
}

// CancelUpdate 取消下载或撤销等待中的重启。
func (a *App) CancelUpdate() {
	if a.upd != nil {
		a.upd.Cancel()
	}
}

// ApplyUpdate 替换可执行文件并重启（通话中挂起等待）。
func (a *App) ApplyUpdate() error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Apply()
}

// UpdateStatus 返回更新状态快照。
func (a *App) UpdateStatus() UpdateStatus {
	if a.upd == nil {
		return UpdateStatus{Phase: UpdateIdle}
	}
	return a.upd.Status()
}

// ---------------------------------------------------------------------------
// 更新站点配置（REQ-UPD-017）：软电话自己按周期查询
// ---------------------------------------------------------------------------

// SiteConfigSnapshot 查询站点配置快照（core 与 updater 之间的传参类型）。
type SiteConfigSnapshot struct {
	URL        string
	AppID      string
	Interval   time.Duration
	Prerelease bool
}

// SetUpdateConfig 保存并应用自动更新配置（配置页与 WS system.setUpdateConfig 共用）。
// 落盘后立即下发给查询器（配了站点就立刻生效并查一次），并广播
// system.updateConfig 供桌面与网页同步。
func (a *App) SetUpdateConfig(cfg config.UpdateConfig) error {
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	if cfg.URL != "" {
		if u, err := url.Parse(cfg.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w：更新站点必须是 http/https 地址", ErrUpdateInvalid)
		}
	}
	if cfg.AppID == "" {
		cfg.AppID = "mti-sip-phone"
	}
	cfg.IntervalMins = config.ClampUpdateIntervalMins(cfg.IntervalMins)

	a.mu.Lock()
	a.cfg.Update = cfg
	a.mu.Unlock()

	if a.cfgPath != "" {
		if err := a.cfg.Save(a.cfgPath); err != nil {
			return fmt.Errorf("保存配置失败: %w", err)
		}
	}
	if a.upd != nil {
		a.upd.SetUpdateConfig(SiteConfigSnapshot{
			URL:        cfg.URL,
			AppID:      cfg.AppID,
			Interval:   cfg.Interval(),
			Prerelease: cfg.Prerelease,
		})
	}
	a.bus.Publish("system.updateConfig", cfg)
	return nil
}

// UpdateConfig 当前更新配置快照。
func (a *App) UpdateConfig() config.UpdateConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Update
}

// CheckUpdateNow 立即查询一次更新站点（界面「立即检查」用）。
// 未配置站点时返回 Skipped 结果；未接入更新能力时返回 ErrUpdateUnavailable。
func (a *App) CheckUpdateNow(ctx context.Context) (CheckResult, error) {
	if a.upd == nil {
		return CheckResult{}, ErrUpdateUnavailable
	}
	return a.upd.CheckNow(ctx)
}

// CheckResult 一次查询的结果（界面提示用）。
type CheckResult struct {
	Skipped       bool
	UpToDate      bool
	LatestVersion string
	Notified      bool
	Reason        string
	Err           error
}

// Text 结果的中文描述（界面提示用）。
func (r CheckResult) Text() string {
	switch {
	case r.Skipped:
		return "未配置更新站点，未检查"
	case r.Err != nil:
		return "检查失败：" + r.Err.Error()
	case r.Notified:
		return "发现新版本 v" + r.LatestVersion + "，请在提示窗中确认更新"
	case r.Reason == "not_newer":
		return "已是最新版本"
	case r.Reason == "already_notified":
		return "已是最新版本（v" + r.LatestVersion + " 已提示过）"
	case r.Reason == "no_download":
		return "站点没有本平台的安装包"
	}
	return "已是最新版本"
}
```

### 附录 G：WS 接口分支 `internal/wsapi/handler.go`（节选：`system.setUpdateConfig` 与 `system.notifyUpdate`）

```go
	case MethodSetUpdateConfig:
		// 自动更新配置（REQ-UPD-018）：网页可写入站点/应用 ID/周期/预览版，
		// 软电话据此自己定时检查（配了站点才查）。字段**都是可选的**，
		// 只改传了的（用指针区分"没传"与"传了零值"），写入后落盘并广播
		// system.updateConfig，桌面配置页与其它网页同步。
		var p struct {
			URL             *string `json:"url"`
			AppID           *string `json:"appId"`
			IntervalMinutes *int    `json:"intervalMinutes"`
			Prerelease      *bool   `json:"prerelease"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		cfg := s.app.UpdateConfig()
		if p.URL != nil {
			cfg.URL = *p.URL
		}
		if p.AppID != nil {
			cfg.AppID = *p.AppID
		}
		if p.IntervalMinutes != nil {
			cfg.IntervalMins = *p.IntervalMinutes
		}
		if p.Prerelease != nil {
			cfg.Prerelease = *p.Prerelease
		}
		if err := s.app.SetUpdateConfig(cfg); err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, s.app.UpdateConfig())


```

 `internal/wsapi/handler.go`（节选）


### 附录 H：网页 SDK 的更新部分 `web/sdk/sip-phone-sdk.js`（节选）

> 只保留**强制推送**与**写入更新配置**两块（查询已搬到软电话侧，REQ-UPD-019）；构造参数里的更新项见构造函数开头。

```js
    /**
     * **强制推送**更新：把指定版本告知软电话，软电话提示用户、由用户确认后更新。
     *
     * 这是 SDK 唯一的更新相关能力——**不受版本高低与预览版约束**（内部恒带
     * force）。「有没有新版本」由**软电话自己**按配置的周期查询更新站点
     * （配置见 setUpdateConfig）；网页若要自己查版本，请自行请求站点接口。
     *
     * @param {{version: string, url: string, sha256?: string, notes?: string,
     *          os?: string, arch?: string}} info 新版本信息
     * @returns {Promise<{phase: string, version: string, currentVersion: string}>}
     */
    notifyUpdate(info) {
      info = info || {};
      if (!info.version || !info.url) {
        return Promise.reject(new Error('notifyUpdate 需要 version 与 url'));
      }
      return this._request('system.notifyUpdate', {
        version: info.version,
        url: info.url,
        sha256: info.sha256,
        notes: info.notes,
        os: info.os,
        arch: info.arch,
        force: true // 本方法就是强制推送通道
      });
    }

    /**
     * 写入软电话的**自动更新配置**（REQ-UPD-018）：软电话据此自己定时查询更新
     * 站点（配了站点才会查），查到新版本弹提示窗。字段都是可选的，只改传入的。
     *
     * 构造参数里给了 `updateUrl` / `updateAppId` / `updateIntervalMinutes` /
     * `updatePrerelease` 时，连上软电话后会自动写入一次（业务前端统一管理策略）。
     *
     * @param {{url?: string, appId?: string, intervalMinutes?: number,
     *          prerelease?: boolean}} cfg url 空 = 不自动检查；周期 1–1440 分钟
     * @returns {Promise<{url: string, appId: string, intervalMinutes: number,
     *          prerelease: boolean}>} 生效后的完整配置
     */
    setUpdateConfig(cfg) {
      cfg = cfg || {};
      const params = {};
      if (cfg.url !== undefined) { params.url = String(cfg.url || '').trim(); }
      if (cfg.appId !== undefined) { params.appId = String(cfg.appId || '').trim(); }
      if (cfg.intervalMinutes !== undefined) { params.intervalMinutes = Math.round(cfg.intervalMinutes); }
      if (cfg.prerelease !== undefined) { params.prerelease = !!cfg.prerelease; }
      this._updateConfigWanted = Object.assign({}, this._updateConfigWanted, params);
      if (!this._connected) {
        return Promise.reject(new Error('尚未连接软电话'));
      }
      return this._request('system.setUpdateConfig', params);
    }

    /** _applyUpdateConfigOnConnect 连上后把构造参数里的更新配置写入软电话。 */
    _applyUpdateConfigOnConnect() {
      if (!this._updateConfigWanted) { return; }
      this._request('system.setUpdateConfig', this._updateConfigWanted).catch(() => {
        // 写入失败不影响其它功能（页面可从 system.state.updateConfig 看到实际值）
      });
    }
```

### 附录 I：测试页的更新卡片 `web/test.html`（节选）

> 查询由**页面自己 fetch** 站点接口（不再经 SDK），另加「写入软电话配置」。

HTML：

```html
      <!-- 更新站点：**由本页自己 fetch** 查版本（软电话侧的自动检查由它自己的配置驱动，
           见下方「写入软电话配置」） -->
      <div class="row">
        <label>更新站点</label>
        <input type="text" id="updSite" value="https://update.wuzm219.cn" placeholder="如 https://update.example.com">
        <button id="btnCheckUpdate">检查更新</button>
      </div>
      <div class="row">
        <label>应用 ID</label>
        <input type="text" id="updAppId" value="mti-sip-phone">
        <label>周期(分)</label>
        <input type="number" id="updInterval" value="5" min="1" max="1440" style="min-width:70px;flex:none;width:80px">
      </div>
      <div class="row">
        <label class="check"><input type="checkbox" id="updPrerelease">接受预览版</label>
        <button id="btnWriteUpdCfg" class="gray">写入软电话配置</button>
      </div>
      <p class="state-line" id="updLine">软电话自动检查：见下方「写入软电话配置」与软电话设置页。</p>
      <!-- 站点版本列表（点「检查更新」拉取）：每行 = 版本 + 黄色「推送」按钮 -->
      <ul class="upd-list" id="updVersions"></ul>
      <p class="state-line">「推送」是强制推送：跳过版本比较，低版本/同版本也能推给软电话（联调用）。</p>
      <p class="state-line">站点用自签名证书时，浏览器必须先信任该证书（否则查询会被证书校验拦下）；站点若只开了 HTTP 端口且会 301 跳到 HTTPS，这里要直接填 https 地址。</p>
```

JS：

```js
  // ---------------- 自动更新（页面自己查站点，软电话侧的检查由它自己的配置驱动）----------------
  // 说明：查询更新站点**不再由 SDK 负责**（REQ-UPD-017：软电话自己按配置的周期查）。
  // 本页保留「查版本 + 强制推送」的联调能力，查询用页面自己的 fetch 实现。

  var UPDATE_FETCH_TIMEOUT = 10000;

  // 站点请求：GET {站点}/api/v1/apps/{应用}{路径}?参数
  function siteFetch(path, params, mirror) {
    var base = $('updSite').value.trim().replace(/\/+$/, '');
    if (!base) {
      return Promise.reject(new Error('未填更新站点'));
    }
    var appId = $('updAppId').value.trim() || 'mti-sip-phone';
    var qs = Object.keys(params).map(function (k) {
      return k + '=' + encodeURIComponent(params[k]);
    }).join('&');
    var url = base + '/api/v1/apps/' + encodeURIComponent(appId) + path + (qs ? '?' + qs : '');
    var ctrl = (typeof AbortController !== 'undefined') ? new AbortController() : null;
    var timer = ctrl ? setTimeout(function () { ctrl.abort(); }, UPDATE_FETCH_TIMEOUT) : null;
    return fetch(url, ctrl ? { signal: ctrl.signal } : {}).then(function (resp) {
      if (timer) { clearTimeout(timer); }
      if (resp.status === 404) { return null; } // /latest 无匹配版本 = 没有，不算错误
      if (!resp.ok) { throw new Error('HTTP ' + resp.status); }
      return resp.json();
    }, function (e) {
      if (timer) { clearTimeout(timer); }
      throw (e && e.name === 'AbortError') ? new Error('请求超时') : e;
    }).then(function (data) {
      if (!data || !mirror) { return data; }
      // 拍平成 {version, prerelease, notes, url, sha256}（与列表项一致）
      var r = data.release || {};
      var d = data.download || {};
      return {
        version: r.version || '',
        title: r.title || '',
        prerelease: !!r.prerelease,
        publishedAt: r.publishedAt || '',
        notes: r.notes || '',
        mandatory: !!r.mandatory,
        file: d.file || '',
        url: d.url || '',
        sha256: d.sha256 || '',
        size: d.size || 0
      };
    });
  }

  // 拉「最新版本（含预览版）」与「最新稳定版」。注意 prerelease 必须显式传：
  // 站点默认就是 false，不传的话两次请求会拿到同一个结果。
  function fetchSiteReleases() {
    var params = {};
    if (buildInfo.os) { params.os = buildInfo.os; }
    if (buildInfo.arch) { params.arch = buildInfo.arch; }
    return Promise.all([
      siteFetch('/latest', Object.assign({ prerelease: 'true' }, params), true),
      siteFetch('/latest', Object.assign({ prerelease: 'false' }, params), true)
    ]).then(function (res) {
      return { current: buildInfo.version || '', latest: res[0], latestStable: res[1] };
    });
  }

  // 「写入软电话配置」：把卡片里的站点/应用 ID/周期/预览版写进软电话（REQ-UPD-018），
  // 之后**软电话自己**会按周期检查并在有更新时弹提示窗。
  $('btnWriteUpdCfg').onclick = function () {
    var interval = parseInt($('updInterval').value, 10);
    if (!interval || interval < 1) { interval = 5; }
    if (interval > 1440) { interval = 1440; }
    sdk.setUpdateConfig({
      url: $('updSite').value.trim(),
      appId: $('updAppId').value.trim() || 'mti-sip-phone',
      intervalMinutes: interval,
      prerelease: $('updPrerelease').checked
    }).then(function (cfg) {
      $('updInterval').value = cfg.intervalMinutes;
      $('updLine').textContent = cfg.url
        ? '软电话自动检查：' + cfg.url + '（应用 ' + cfg.appId + '，每 ' + cfg.intervalMinutes + ' 分钟）'
        : '软电话自动检查：未启用（更新站点为空）';
      addLog('ok', '已写入软电话更新配置：' + (cfg.url ? cfg.url + '，每 ' + cfg.intervalMinutes + ' 分钟' : '未启用自动检查'));
    }, function (e) {
      addLog('err', '写入软电话更新配置失败: ' + e.message);
    });
  };

  // 站点版本列表的一项：版本名 + 黄色「推送」按钮（强制推送，跳过版本比较）
  function versionItem(label, item, currentVersion) {
    var li = document.createElement('li');
    var name = document.createElement('span');
    name.className = 'txt';
    if (!item) {
      name.textContent = label + '：—（站点上没有）';
      li.appendChild(name);
      return li;
    }
    // 版本号加粗（用 DOM 节点拼，不拼 HTML），标签与括注用普通字重
    name.appendChild(document.createTextNode(label + '：'));
    var ver = document.createElement('b');
    ver.textContent = 'v' + item.version;
    name.appendChild(ver);
    var note = '';
    if (item.prerelease) { note += '（预发布）'; }
    if (item.version === currentVersion) { note += '（与当前相同）'; }
    if (note) { name.appendChild(document.createTextNode(note)); }
    li.appendChild(name);

    var btn = document.createElement('button');
    btn.className = 'yellow';
    btn.textContent = '推送';
    if (!item.url) {
      btn.disabled = true;
      btn.title = '站点上该版本没有本平台的产物';
    }
    btn.onclick = function () {
      btn.disabled = true;
      addLog('info', '强制推送 v' + item.version + '（忽略版本比较）…');
      sdk.notifyUpdate({
        version: item.version,
        url: item.url,
        sha256: item.sha256,
        notes: item.notes
      }).then(function (res) {
        addLog('ok', '已强制推送 v' + res.version + '（当前 v' + res.currentVersion +
          '，状态 ' + res.phase + '）——请在软电话上确认更新');
      }, function (e) {
        addLog('err', '推送失败: ' + e.message + (e.code ? '（' + e.code + '）' : ''));
      }).then(function () { btn.disabled = false; });
    };
    li.appendChild(btn);
    return li;
  }

  $('btnCheckUpdate').onclick = function () {
    var box = $('updVersions');
    box.textContent = '';
    addLog('info', '查询更新站点版本列表…');
    fetchSiteReleases().then(function (r) {
      box.appendChild(versionItem('最新版本', r.latest, r.current));
      box.appendChild(versionItem('最新稳定版', r.latestStable, r.current));
      addLog('ok', '站点版本：最新 ' + (r.latest ? 'v' + r.latest.version : '—') +
        '，最新稳定 ' + (r.latestStable ? 'v' + r.latestStable.version : '—') +
        '（当前 v' + r.current + '）');
    }).catch(function (e) {
      addLog('err', '查询版本列表失败: ' + e.message);
    });
  };
```
