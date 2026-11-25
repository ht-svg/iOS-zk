# 海涛中控 v0.1

这是基于所给项目中可读代码与配置重新实现的独立 Go 项目，不调用原 Python 主程序。提供本机 Web 管理界面，支持：

- 通过 Windows usbmuxd 查询 USB/网络连接的 iPhone
- 管理页面打开后自动连接当前选中的首台设备并开启投屏；设备稍后接入且当前主控没有可用会话时，会自动选中并尝试连接（失败后 15 秒内不重复尝试）
- 界面按所给项目的前端一比一复刻：深色背景、顶部通知条、左侧操作按钮栏、主屏画面、右侧设备小窗和底部输入/群控区
- 主屏尺寸随窗口自适应：高度取 `clamp(360px, 100vh-200px, 900px)`，宽度按 375:667 设备比例推导，大显示器上会自动放大，矮窗口下也不会把底部输入区挤出屏幕；实时显示设备画面并支持点击/拖动；在画面上右键会返回设备主屏幕，并屏蔽浏览器原生右键菜单；未载入画面时常驻授权提示遮罩
- 当前主控在大屏显示，其他设备以小窗横向排列在主屏右侧，点击小窗切换主控。“群控”开关开启后，触控、滑动、文本和系统操作会同步到全部已连接设备
- 群控操作走 `POST /api/batch`：浏览器只发一个请求，后端按最多 50 台并发扇出（单请求最多 128 台），返回每台设备的成功/失败、错误码和名称；部分失败会在界面显示成功数和失败设备，不再静默吞掉
- 为设备设置名称与本机 Appium 地址
- 优先创建 Appium 会话；Appium 不可用时，自动检查并连接手机上已运行的 WebDriverAgent（WDA）。WDA 就绪但没有活动会话时（`/status` 返回 `sessionId: null`，常见于 WDA 刚启动），会直接向 WDA 新建一个会话，而不是判定失败
- 通过只监听 `127.0.0.1` 的 usbmux 转发访问设备 WDA，不会把 WDA 端口暴露到局域网
- 通过 WDA 原生 MJPEG 实时流投屏并支持点击/拖动操作；实际帧率受设备、WDA 和 USB 链路限制；视频流中断后按 1–15 秒退避重连
- 「调整分辨率」按钮打开“画面与帧率”面板：可调投屏帧率（10–60 FPS）与压缩画质（25/50/75/90），设置保存在浏览器本地并立即重开视频流生效。iOS 不允许修改系统分辨率，因此该面板调整的是投屏带宽与流畅度
- 其他设备的小窗使用服务端生成的 180px 宽 JPEG 缩略图，单设备请求超时 8 秒，失败后 15 秒退避，避免一台设备卡住全部预览
- “发送不同内容”弹窗支持逐台填写文本，走 `POST /api/batch` 的 `targets` 形式分别下发
- 自动将截图像素坐标换算为 iOS 窗口逻辑坐标，避免高分辨率截图造成点击偏移；窗口尺寸缓存 10 秒，横竖屏切换后自动失效
- 输入文本、读写设备剪贴板；设备轮询刷新时保留尚未发送的文本。文本框按 Enter 直接发送（中文输入法 composing 状态不会误触发），也可点击“回车键”向手机发送 Return 键，群控时同样生效
- 锁屏、解锁、回到主屏幕、切换横竖屏；Appium 模式下也可尝试重启
- 后端仍保留音量、未读数、设备名称/分组/Appium 地址设置等接口，可通过 API 直接调用
- 设备授权：一码一手机。后台随机生成授权码并写入项目目录的 `授权码.xlsx`，中控启动时扫描表格并与后台绑定结果比对，绑定成功后在 `授权备份.json` 留底；断网时用备份与表格比对。校验不通过时主屏显示授权遮罩并**阻断屏幕上的所有鼠标操作**（点击、拖动、右键均无效），操作接口同时返回 403

## 运行条件

- Windows 10/11 与 Go 1.22+
- 安装 Apple 设备支持组件，并通过 USB 连接且信任 iPhone；设备发现通过本机 `127.0.0.1:27015` usbmuxd 服务
- 可选：启动兼容的本机 Appium 服务及 XCUITest/WebDriverAgent。Appium 默认地址为 `http://127.0.0.1:4723`，可在界面按设备修改
- 如果没有 Appium，设备上必须已经运行有效的 WebDriverAgent。程序不会自动安装或签名 WDA
- 设备需要处于已配对/可自动化状态；Appium/WDA 操作受 iOS 版本、开发者镜像和签名影响

标准 XCUITest driver 通常需要 macOS/Xcode 来构建和签名 WebDriverAgent。当前已验证的无 Appium 路径要求设备上已有正在运行的 WDA；如设备未预装/未运行 WDA，请先在兼容环境准备好它。Appium 官方 Windows 非 macOS 主机支持有限，要求真机 iOS 18+；较旧系统可尝试连接已运行的 WDA，但不保证所有 WDA 操作可用。

## 启动

```powershell
go test ./...
go build -o 海涛中控v0.1.exe .
.\海涛中控v0.1.exe
```

启动后程序会尝试自动打开浏览器访问管理台；页面发现设备后会自动连接默认选中的设备并开启投屏。如果没有自动打开，请手动访问 `http://127.0.0.1:8787`。服务仅监听回环地址，不向局域网开放。设备配置写入 `%LOCALAPPDATA%\GoPhoneManager\config.json`。

## 设备授权

**一个授权码对应一台手机。** 授权码由后台（`license-admin/`）随机生成，流程如下：

1. 在后台生成授权码，后台会把它们写入 `授权码.xlsx`（工作表名为「授权码」，A 列为授权码）。
2. 把 `授权码.xlsx` 放到中控程序所在目录（可用环境变量 `GO_PHONE_MANAGER_LICENSE_TABLE` 指定其它路径）。
3. 在 `config.json` 里填写后台地址：

```json
{
  "licenseServer": "http://你的服务器:8090"
}
```

4. 启动中控。它对每台已连接的手机分别校验：
   - 表格里有已绑定这台手机的码 → 直接放行；
   - 否则取一个未绑定的码发起绑定，后台记录「码 → 该手机 UDID」并返回结果，同时把这对关系写入本地备份 `授权备份.json`；
   - 表格中的码都已被其它手机占用 → 这台手机判定失败。

### 断网时的本地备份

首次绑定成功后，中控会把「手机 UDID → 授权码 → 后台签名」写入程序目录下的 `授权备份.json`（可用 `GO_PHONE_MANAGER_LICENSE_BACKUP` 指定路径）。连不上后台时改为校验这份备份：

- 备份里存在这台手机、签名能通过内置公钥验签、且对应的授权码**仍在 `授权码.xlsx` 中** → 放行；
- 备份里没有这台手机、签名对不上、或码已被从表格删除 → 遮罩，禁止操作。

签名由后台 Ed25519 私钥生成，客户端只内置公钥。因此**手工修改 `授权备份.json` 或伪造授权码都不再生效**——没有私钥就造不出能验签的签名。

### 有效期

后台生成授权码时可填「有效期(天)」，从生成时刻起算，留空或 0 为永久。到期后：

- 该码不能再绑定到新设备；
- 已绑定的设备在下次校验时被判为未授权，遮罩提示「授权码已过期」；
- 离线状态下同样按签名里的到期时间判定，改本地文件无法延长。

到期时间参与签名（`码|UDID|到期时间`），所以手工延长 `授权备份.json` 里的时间会直接验签失败。

### 授权服务器地址要求

`licenseServer` 必须是 `https://`，只有本机地址（`127.0.0.1`、`localhost`）允许用 `http://`。配置成远程明文 HTTP 会直接判定未授权并提示，避免中间人伪造绑定响应。

地址的解析优先级：`config.json` → 环境变量 `GO_PHONE_MANAGER_LICENSE_SERVER` → **编译期内置值**。

### 云端部署（正式发售用这个）

后台是独立程序，部署在一台服务器上给所有客户共用，不需要装在客户电脑上。编译客户端时把地址和令牌烧进去，客户拿到 exe 就能直接连：

```bash
go build -ldflags "-s -w \
  -X main.defaultLicenseServer=https://license.yourdomain.com \
  -X main.defaultLicenseToken=后台生成的令牌 \
  -X main.appVersion=0.1.0" -o 海涛中控.exe .
```

烧进去之后客户端**不需要任何配置文件**。详细的服务器部署步骤（systemd、Caddy 自动 HTTPS、备份、排错）见 [`deploy/DEPLOY.md`](deploy/DEPLOY.md)。

如果后台启用了 `LICENSE_API_TOKEN`，在 `config.json` 里一并填写：

```json
{
  "licenseServer": "https://your-server:8090",
  "licenseToken": "后台设置的令牌"
}
```

| 情况 | 结果 |
| --- | --- |
| 表格缺失 / 无有效授权码 | 遮罩，禁止操作 |
| 该手机的码已被其它手机绑定 | 遮罩，提示联系管理员 |
| 后台连不上，备份与表格一致且签名有效 | 放行（离线模式） |
| 后台连不上，备份被篡改或与表格对不上 | 遮罩，禁止操作 |
| 授权服务器配成远程 HTTP | 遮罩，提示改用 https |
| 校验通过 | 遮罩消失，可正常操作 |

`GET /api/license` 返回当前状态：`offline`、`reason`、`server`、`tablePath`、`backupPath`、`codeCount`、`deviceCount`、`checkedAt`，以及 `devices`（每台手机的 `authorized` / `code` / `source` / `reason`）。`GET /api/devices` 里每台设备也带 `authorized`、`licenseCode`、`licenseReason`。界面每 10 秒随设备列表刷新，后台每 60 秒重新校验一次。左侧「填写注册码」按钮查看这份授权详情，「刷新授权」立即重新扫描表格并与后台比对。

未授权设备的所有操作接口（点击、滑动、输入、系统操作、应用管理、剪贴板写入、批量控制）都会返回 `403 unauthorized`，即使绕过界面直接调 API 也无法驱动手机。

未授权时主屏遮罩可见并拦截全部鼠标事件（点击、拖动、右键均无效）；遮罩也会在「没有设备」「投屏已关闭」「画面载入中」时出现，此时同样不允许操作屏幕。

## 投屏画面参数

`GET /api/devices/{udid}/stream` 支持三个查询参数，缺省时沿用各模式原有默认值：

| 参数 | 范围 | 说明 |
| --- | --- | --- |
| `fps` | 1–60 | 帧率上限。WDA 模式写入 `mjpegServerFramerate`，Appium 模式控制截图轮询间隔 |
| `quality` | 1–100 | JPEG 压缩画质。WDA 模式写入 `mjpegServerScreenshotQuality`，Appium 模式用于本地重编码 |
| `scale` | 25–100 | 设备侧缩放。实测 WDA 8.7.11 在小于 100 时会改用另一条编码路径，单帧反而更大，因此界面不暴露该参数 |

实测（iPhone，WDA 8.7.11）：默认 60 FPS / 画质 25 时约 38 FPS、56 KB/帧；改为 10 FPS / 画质 25 后稳定在约 9.6 FPS，带宽降到约 1/5。

## 批量控制接口

`POST /api/batch`，请求体：

```json
{
  "udids": ["00008030-001A11261489802E", "00008030-001B22372590913F"],
  "operation": "tap",
  "body": {"x": 195, "y": 422, "imageWidth": 1170, "imageHeight": 2532}
}
```

`operation` 支持 `tap`、`swipe`、`type`、`actions`、`clipboard`、`app`；其中 `actions` 的 `action` 可为 `lock`、`unlock`、`home`、`volumeUp`、`volumeDown`、`reboot`、`orientation`、`enter`，`app` 的 `action` 可为 `open`、`uninstall`（`bundleId` 指定应用）。响应固定为 HTTP 200（参数错误除外），并返回每台设备的结果：

```json
{
  "ok": false,
  "total": 2,
  "succeeded": 1,
  "failed": 1,
  "results": [
    {"udid": "00008030-001A11261489802E", "name": "手机 A", "ok": true},
    {"udid": "00008030-001B22372590913F", "name": "手机 B", "ok": false, "error": "设备会话已失效，正在重新连接", "code": "session_lost"}
  ]
}
```

### 差异化群发（每台不同内容）

`POST /api/batch` 除上面的 `udids + body`（所有设备共用一份内容）外，还支持 `targets` 逐台指定内容：

```json
{
  "operation": "type",
  "body": {"text": "默认内容，未单独指定的设备使用此内容"},
  "targets": [
    {"udid": "00008030-001A11261489802E", "body": {"text": "发给 A 的内容"}},
    {"udid": "00008030-001B22372590913F", "body": {"text": "发给 B 的内容"}},
    {"udid": "00008030-001C33373590914G"}
  ]
}
```

- 每台设备可带自己的 `body`；未带 `body` 的设备自动回退到顶层 `body` 作为模板。
- 单台设备的内容校验失败（如文本过长）只标记该台为 `bad_request`，不影响其他设备执行，结果顺序与 `targets` 一致。
- 页面顶栏「差异化群发」面板提供：全部/分组/手动三种目标选择、默认内容模板、`{{name}}`/`{{index}}`/`{{udid}}` 变量替换、按行批量填充、逐台编辑与逐台结果展示。

### 应用打开与卸载

- 单设备：`POST /api/devices/{udid}/app`，body 为 `{"action":"open"|"uninstall","bundleId":"com.example.App"}`。
- 批量：通过 `/api/batch` 的 `operation:"app"` 同样支持。
- 页面「应用」区内置常用 App 名单，也可直接输入 Bundle ID；卸载在 WDA 直连模式下返回 `unsupported`，需用 Appium 会话。

### 设备分组

- 在「设备信息」里为设备填写分组名并保存，配置持久化到 `%LOCALAPPDATA%\GoPhoneManager\config.json`。
- 设备列表侧栏按分组归类显示；差异化群发面板支持「按分组」选择目标设备。

所有失败响应（包括普通 HTTP 接口和批量结果）都带同一个 `code` 字段：

| `code` | HTTP 状态 | 含义 |
|---|---|---|
| `bad_request` | 400 | 参数或请求体错误 |
| `session_required` | 409 | 设备尚未建立 Appium/WDA 会话 |
| `session_lost` | 409 | 会话已失效，本地缓存已清理 |
| `unsupported` | 501 | 当前模式不支持该操作 |
| `upstream_error` | 502 | Appium/WDA 上游请求失败 |
| `device_error` | — | 未能归类的设备操作失败 |
| `internal_error` | 500 | 服务内部错误 |
| `service_unavailable` | 503 | 服务不可用 |

前端会对 `session_lost` / `session_required` 的设备触发自动重连，其余错误码按设备名展示，避免把上游失败误判为会话问题。

## 可配置项

以下环境变量都有默认值，是否需要设置取决于部署环境：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `GO_PHONE_MANAGER_LISTEN` | `127.0.0.1:8787` | Web 服务监听地址 |
| `GO_PHONE_MANAGER_USB_MUX` | `127.0.0.1:27015` | Windows usbmuxd 地址 |
| `GO_PHONE_MANAGER_APPIUM_URL` | `http://127.0.0.1:4723` | 默认 Appium 地址 |
| `GO_PHONE_MANAGER_WDA_PORT` | `8100` | 设备 WDA 端口 |
| `GO_PHONE_MANAGER_WDA_STREAM_PORT` | `9100` | 设备 MJPEG 端口 |
| `GO_PHONE_MANAGER_HTTP_TIMEOUT` | `45` | Appium/WDA HTTP 请求超时（秒） |
| `GO_PHONE_MANAGER_PROBE_TIMEOUT` | `5` | 会话探活超时（秒） |
| `GO_PHONE_MANAGER_WINDOW_SIZE_TTL` | `10` | 窗口尺寸缓存时间（秒） |
| `GO_PHONE_MANAGER_BATCH_CONCURRENCY` | `50` | 批量操作并发数 |
| `GO_PHONE_MANAGER_MAX_BATCH_DEVICES` | `128` | 单批量请求设备上限 |
| `GO_PHONE_MANAGER_BATCH_TOTAL_TIMEOUT` | `60` | 批量请求总超时（秒） |
| `GO_PHONE_MANAGER_BATCH_DEVICE_TIMEOUT` | `30` | 批量单设备超时（秒） |
| `GO_PHONE_MANAGER_APPIUM_STREAM_FPS` | `15` | Appium 回退投屏帧率 |
| `GO_PHONE_MANAGER_CONFIG_DIR` | `GoPhoneManager` | `%LOCALAPPDATA%` 下的配置目录 |
| `GO_PHONE_MANAGER_CONFIG_FILE` | `config.json` | 配置文件名 |
| `GO_PHONE_MANAGER_CLIENT_NAME` | `GoPhoneManager` | usbmux 协议中的客户端名 |

日志为结构化文本：每个请求输出 `request_id / method / path / status / duration_ms`，设备级事件额外带 `udid` 和 `event`，便于排查并发问题。

## 开发质量门禁

代码已按职责拆分：`main.go`（启动与路由）、`config.go`（配置与设备列表）、`session.go`（会话生命周期）、`api.go`（HTTP 接口）、`operations.go`（设备操作与批量参数处理）、`errors.go`（稳定错误码）、`runtime_config.go`（运行时配置）、`logging.go`（结构化日志），另有 `appium.go`、`usbmux.go`、`wda_relay.go`。

本地检查：

```bash
make check        # gofmt 检查 + go vet + 单元测试
make check-full   # 再加 golangci-lint、race 检测、覆盖率门禁
```

CI 在每次 push / pull request 上运行格式化检查、`go vet`、`golangci-lint`、带 race detector 的测试和覆盖率门禁（当前最低 35%）。竞态检测需要 CGO；本机若没有 C/C++ 编译器，可以直接依赖 CI 执行。

## 已知边界

原项目主界面/后端的 Python 源文件不在交付目录中，原有桌面界面及自动化脚本/任务执行不在本次实现范围内。WDA 模式使用设备端原生 MJPEG 服务并请求 60 FPS，实际帧率受设备、WDA 实现和 USB 链路影响；当前设备实测约 39 FPS，不保证达到 60 FPS。Appium 模式回退为 15 FPS 截图轮询，并重新编码为 JPEG。WDA 直连模式不能执行重启；重启操作仅在 Appium 模式尝试使用 `mobile: reboot` 扩展。设备没有正在运行的 WDA 时，仍需单独准备有效的 WDA/Appium 环境。

会话在每次复用前会做一次探活；Appium/WDA 返回 `invalid session id` 时会立即清理本地会话并让前端自动重连。WDA 会话的探活不依赖 `/status` 是否回传 `sessionId`（多数版本不返回该字段），而是用 `GET /session/{id}/window/size` 判断会话是否仍可用，避免把正常会话误判为失效后反复重建。设备拔出或 usbmuxd 的 `DeviceID` 变化时，本地会话和 WDA 转发会被关闭，避免继续使用失效的转发端口。上述恢复不覆盖 WDA 签名过期、开发者镜像缺失等设备端问题。

usbmux `Connect` 请求里的 `PortNumber` 必须写 `htons(port)` 的结果（与 libusbmuxd、tidevice 一致），在小端主机上表现为 8100 写作 42015、9100 写作 35875。改成十进制端口反而会让手机端直接断开连接，`wda_relay_test.go` 已对此加了回归测试。
