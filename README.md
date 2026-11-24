# 海涛中控 v0.1

这是基于所给项目中可读代码与配置重新实现的独立 Go 项目，不调用原 Python 主程序。提供本机 Web 管理界面，支持：

- 通过 Windows usbmuxd 查询 USB/网络连接的 iPhone
- 管理页面打开后自动连接当前选中的首台设备并开启投屏；设备稍后接入且当前主控没有可用会话时，会自动选中并尝试连接（失败后 15 秒内不重复尝试）
- 将设备切换、主屏幕、系统操作、文本输入和剪贴板集中到响应式控制台布局中，并针对窄屏自动调整
- 主投屏区固定为竖屏 9:16，并按可视窗口限制尺寸；手机画面按自身比例完整缩放，支持横竖屏内容
- 当前主控只在大屏显示，侧边仅以小窗显示其他设备，因此设备数与窗口总数一致；点击小窗切换主控。“控制所有手机”开启时会以最多 3 台并发尝试连接所有设备，并将触控、滑动、文本、剪贴板和系统操作同步到全部手机，关闭时只控制当前手机
- 群控操作走 `POST /api/batch`：浏览器只发一个请求，后端按最多 8 台并发扇出（单请求最多 128 台），返回每台设备的成功/失败、错误码和名称；部分失败会在界面显示成功数和失败设备，不再静默吞掉
- 为设备设置名称与本机 Appium 地址
- 优先创建 Appium 会话；Appium 不可用时，自动检查并连接手机上已运行的 WebDriverAgent（WDA）
- 通过只监听 `127.0.0.1` 的 usbmux 转发访问设备 WDA，不会把 WDA 端口暴露到局域网
- 通过 WDA 原生 MJPEG 实时流投屏并支持点击/拖动操作；WDA 模式请求 60 FPS，实际帧率受设备、WDA 和 USB 链路限制。主控流断开后前端按 1/2/4/8 秒退避重连，最多 5 次
- 侧边小窗使用服务端生成的 180px 宽 JPEG 缩略图轮询，避免每台设备都传全分辨率 PNG；主控仍使用原始 MJPEG
- 自动将截图像素坐标换算为 iOS 窗口逻辑坐标，避免高分辨率截图造成点击偏移；窗口尺寸缓存 10 秒，横竖屏切换后自动失效
- 输入文本、读写设备剪贴板；设备轮询刷新时保留尚未发送的文本
- 锁屏、解锁、回到主屏幕、音量、切换横竖屏；Appium 模式下也可尝试重启
- 读取原项目中已适配的 WhatsApp、微信、短信、抖音、小红书、闲鱼未读数

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

## 批量控制接口

`POST /api/batch`，请求体：

```json
{
  "udids": ["00008030-001A11261489802E", "00008030-001B22372590913F"],
  "operation": "tap",
  "body": {"x": 195, "y": 422, "imageWidth": 1170, "imageHeight": 2532}
}
```

`operation` 支持 `tap`、`swipe`、`type`、`actions`、`clipboard`。响应固定为 HTTP 200（参数错误除外），并返回每台设备的结果：

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
| `GO_PHONE_MANAGER_BATCH_CONCURRENCY` | `8` | 批量操作并发数 |
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

会话在每次复用前会做一次探活；Appium/WDA 返回 `invalid session id` 时会立即清理本地会话并让前端自动重连。设备拔出或 usbmuxd 的 `DeviceID` 变化时，本地会话和 WDA 转发会被关闭，避免继续使用失效的转发端口。上述恢复不覆盖 WDA 签名过期、开发者镜像缺失等设备端问题。
