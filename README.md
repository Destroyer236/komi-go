# Komi Go

Windows 桌面版 GitHub Release 应用商店，Go + Wails v2 + WebView2 + SQLite，UI 为 Material 3 Expressive。

> 版本：0.3.0　平台：Windows 10/11（amd64 / arm64）　不包含 Kotlin/Compose、Android、macOS/Linux 版本。

## 现在能做什么

| 模块 | 已落地能力 |
|---|---|
| 搜索 / 发现 | 热门、新鲜、经典、Windows 四条榜单；人工智能/隐私/网络/媒体/社交/阅读/工具分类；关键词搜索；语言筛选；星标/更新/复刻排序；搜索历史；分页；只看有 Windows 安装资产的仓库 |
| 仓库详情 | README Markdown 渲染与相对链接改写（含 GFM 表格、表格内图片）、Release 折叠列表、资产表、下载、复制链接、打开仓库页/Issues、预发布与缓存标记 |
| 下载 | 2 并发队列、用户暂停/继续/取消、`.part` Range 续传、重启恢复、磁盘预检（包体 ×3）、镜像竞速、停滞 60s 检测、SHA-256 校验、无摘要标记“未校验” |
| 应用库 | 真实安装目录、占用空间、打开应用、卸载（MSI ProductCode / UninstallString）、更新、固定、跳过版本、应用偏好、移除记录、恢复跟踪、导入导出、外部应用扫描与匹配 |
| 认证 | Windows 凭据管理器持久化 token、Device Flow 登录（内置 Komi Go OAuth App，无需自备 client_id）、401 自动清 token 并提示重登 |
| 网络 | 3 次指数退避 + 抖动、429/`Retry-After`、同 URL single-flight、分层 TTL 缓存、5 分钟负缓存、离线 stale、代理设置 |
| 匹配算法 | Release 线识别、`owner/repo#app` 多应用归属、`pickedAssetIndex`、预发布开关、回退旧版本、资产过滤、七条更新判定、versionCode/同 tag 大小守卫 |
| 生态 | GitHub / Codeberg / 自建 Forgejo 客户端；`komi://` 深链（owner/repo、releases/tag、tags、tree、git@、分享链接）；注册表协议注册；单实例转交深链 |
| 桌面 | 托盘、关闭到托盘、开机启动、注册表安装检测、自更新检查/下载/延迟替换、`komi://` 注册、Inno Setup 安装包、amd64/arm64 构建、可选代码签名 |
| UI | M3E 22 色角色、4/12/16/28 圆角、按压形变、wavy 进度、首次引导、亚克力弹窗、上游式个人资料/调整分组设置、贡献活动热力图、只显示带 Release 的个人仓库、代理/镜像/缓存设置、自动检查更新、类型化错误文案、48px 触控目标、reduce-motion |

## 技术栈

| 层 | 选择 | 说明 |
|---|---|---|
| 桌面壳 | Wails v2.17 | WebView2 渲染，无边框窗口，`frontend/dist` 直接 embed |
| 业务 | Go 1.26+ | 纯 `net/http`，无第三方 GitHub SDK；逻辑在 `internal/*` |
| 数据库 | SQLite | `modernc.org/sqlite`，纯 Go、CGO 关闭，`PRAGMA user_version` 迁移 |
| 前端 | 原生 JS | 无 Node 构建；`app.js` 负责路由、渲染、事件订阅 |
| 设计 | M3 Expressive | 颜色角色、surface 层级、shape 4/12/16/28/full、48px 触控 |

## 目录

```
main.go                  Wails 入口、窗口尺寸与 embed
app.go / repo.go         绑定层：搜索、仓库、认证、调度
downloads.go / library.go 下载安装、账本、导入导出、外部扫描
settings.go / desktop.go / tray.go 设置、单实例、托盘、窗口
internal/
  catalog/               领域模型
  github/                REST 客户端、缓存、重试、Forgejo、深链
  matcher/               资产评分、Release 线、更新七条判定
  platform/              数据目录、文件名净化、下载 URL 策略、代理
  ledger/                SQLite v2：installed_app / download_task / http_cache / settings
  download/              .part + Range 续传、镜像、SHA-256、停滞检测
  installer/             msi / exe / zip / tar.gz / tar / 7z、防 Zip Slip
  winreg/                注册表卸载项、协议、开机启动
  auth/                  Windows Credential Manager
  selfupdate/            自更新检查/下载/延迟替换
frontend/dist/           index.html、styles.css、app.js
packaging/komi-go.iss    Inno Setup 脚本
scripts/build.ps1        多架构构建 + 安装包 + 可选签名
scripts/sign.ps1         signtool 签名与校验
build/windows/           icon.ico（tools/make-icon.ps1 生成）
```

## 构建

```powershell
# 开发检查
go test ./internal/...
go vet ./...

# amd64 exe（输出 dist\KomiGo-0.3.0-amd64.exe）
.\scripts\build.ps1 -Arch amd64 -SkipTests

# arm64 exe
.\scripts\build.ps1 -Arch arm64 -SkipTests

# 两种架构 exe + Inno 安装包
.\scripts\build.ps1 -Arch all -Installer

# 构建并签名（需要证书）
$env:KOMI_SIGN_CERT = "D:\certs\komi.pfx"
$env:KOMI_SIGN_CERT_PASSWORD = "..."
.\scripts\build.ps1 -Arch amd64 -Installer -Sign
```

要求：Go 1.26+、Wails CLI v2、WebView2 Runtime（Win11 自带）、Inno Setup 6（只有 `-Installer` 需要）、Windows SDK Signing Tools（只有 `-Sign` 需要）。

`scripts/sign.ps1` 也支持 `KOMI_SIGN_THUMBPRINT` 或 `KOMI_SIGN_SUBJECT` 从证书存储选证书；默认时间戳 `http://timestamp.digicert.com`。

## 产物

- `dist\KomiGo-0.3.0-amd64.exe` / `KomiGo-0.3.0-arm64.exe`
- `dist\KomiGo-0.3.0-amd64-setup.exe` / `KomiGo-0.3.0-arm64-setup.exe`
- exe 与安装包的 Windows 文件属性均为 `Komi Go` / `0.3.0`（安装包为 `0.3.0.0`）；`scripts/build.ps1` 会在构建后断言版本元数据，防止空版本产物再次流出。
- 安装包为**当前用户安装**，默认目录 `%LOCALAPPDATA%\Programs\Komi Go`，不要求管理员权限；注册 `komi://`、可选桌面快捷方式与开机启动。

## 数据与安全

- 数据目录：`%LOCALAPPDATA%\KomiGo`，含 `komi.db`（WAL）、`downloads\`、`apps\`、`logs\`。
- token 存在 Windows Credential Manager（target `komi-go/github-token`），不写配置文件；`GITHUB_TOKEN` 环境变量优先，便于 CI/高级用户。
- 下载只接受 https、无 userinfo、非内网/本机地址；自建 Forgejo 的公网域名允许，因为安装包来源本身就是用户选择的仓库。
- Release 资产会做 `matcher.Score` 与 `IsJunk` 二次校验；无 SHA-256 的包安装成功后仍提示“未校验”。
- zip/tar/tar.gz/7z 解压拒绝绝对路径、盘符与 `..`，单文件与总解压量有上限。
- 卸载只调用系统登记的 MSI/UninstallString；应用数据目录不随卸载删除。

## 自更新与外部配置

- 自更新仓库在设置项 `self_update.repo`（`owner/repo`），默认空，必须由发布者填写；发布 Release 时上传名字含 `windows`/`amd64`/`x64`/`arm64` 的 `.exe`、`.msi` 或 `.zip`。
- Device Flow 已内置 Komi Go 的 OAuth App client_id（设备码流程不包含 client_secret），仍可在 `github.client_id` 中覆盖为自己的应用；PAT 登录始终可用。
- 代码签名需要发布者自己的证书；没有证书时构建产物是未签名版本。
- Inno 安装包当前使用英文安装向导（Inno Setup 6 默认语言包不含简体中文），应用内界面是中文。

## 已知边界

- 仅 Windows；没有 macOS/Linux 打包。
- 自更新只支持 GitHub 仓库，Forgejo 仓库的客户端能力不用于自更新。
- 安装 EXE 后依赖注册表出现 DisplayName 来确认成功；某些绿色版/自定义安装器不会写注册表，会停在“待确认”并可稍后同步。
- 7z 解压复用系统 `tar.exe`（Win10 1803+ 自带）；极老系统可能不可用。
