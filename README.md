# EasyDesktop

> Windows 桌面路径一键切换工具 — 支持全局热键、WebView2 图形界面与命令行交互模式

---

## 功能特性

- **一键切换桌面路径** — 通过注册表 + `SHSetKnownFolderPath` 修改 Windows Desktop 文件夹指向，立即生效
- **全局热键 `Ctrl+E`** — 随时呼出/隐藏浮动选择窗口，不打断当前工作流
- **WebView2 图形界面** — 无边框、置顶浮窗，支持搜索、置顶、删除历史记录
- **命令行交互模式** — 终端内键盘导航，支持目录预览、重命名、置顶、删除
- **历史记录管理** — 自动记录切换历史，置顶条目优先显示，JSON 持久化存储
- **系统托盘** — 后台常驻，右键菜单快速操作

---

## 快速开始

### 直接切换桌面路径

```
easydesktop <路径>
```

例如：

```
easydesktop D:\Work
easydesktop C:\Users\me\Projects
```

### 打开图形界面

```
easydesktop
easydesktop --gui
```

或按 **`Ctrl+E`** 呼出浮动窗口（需 `easydesktop-ui.exe` 在后台运行）。

### 命令行交互选择器

```
easydesktop -r
```

| 快捷键 | 功能 |
|--------|------|
| `↑` / `↓` | 导航 |
| `Enter` | 切换到选中目录 |
| `Space` | 预览目录内容 |
| `Ctrl+R` | 重命名记录 |
| `Ctrl+P` | 置顶 / 取消置顶 |
| `Ctrl+D` | 删除记录 |
| `Ctrl+A` | 显示全部 / 清除搜索 |
| `Esc` | 退出 |

---

## 命令行参考

```
easydesktop                     打开图形界面
easydesktop <路径>              切换桌面到目标路径
easydesktop --gui               打开图形界面
easydesktop -r [序号]           交互选择历史，或按序号直接切换
easydesktop --pin <路径|序号>   置顶 / 取消置顶
easydesktop --remove <路径|序号> 删除历史记录
easydesktop --move <from> <to>  调整历史顺序（序号从 1 开始）
```

---

## 项目结构

```
easydesktop/
├── main.go          # CLI 主程序（桌面切换 + 交互选择器）
└── ui/
    └── main_ui.go   # WebView2 图形界面（热键 + 托盘 + 浮动窗口）
```

两个可执行文件需放在同一目录下：

| 文件 | 说明 |
|------|------|
| `easydesktop.exe` | CLI 工具，负责实际切换桌面路径 |
| `easydesktop-ui.exe` | 图形界面，后台常驻，提供热键与托盘 |

---

## 构建

需要 Go 1.21+，以及 Windows 系统（依赖 Win32 API）。

```bash
# 构建 CLI
go build -o easydesktop.exe .

# 构建 GUI（无控制台窗口）
cd ui
go build -ldflags="-H windowsgui" -o easydesktop-ui.exe .
```

GUI 依赖 [go-webview2](https://github.com/jchv/go-webview2)，运行时需要系统已安装 **WebView2 Runtime**（Windows 10/11 通常已内置）。

---

## 注意事项

- 切换桌面路径**仅修改路径映射**，不会自动迁移现有桌面文件
- 切换后 Explorer 会自动重启以使更改生效
- 需要当前用户对注册表 `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\Shell Folders` 有写权限

---

## License

MIT
