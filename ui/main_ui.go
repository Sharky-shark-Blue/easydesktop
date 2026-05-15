package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
)

// ─── Win32 常量 ───────────────────────────────────────────────
const (
	SW_HIDE            = 0
	SW_SHOW            = 5
	WM_HOTKEY          = 0x0312
	WM_APP             = 0x8000
	WM_TRAYICON        = WM_APP + 1
	WM_DESTROY         = 0x0002
	WM_LBUTTONUP       = 0x0202
	WM_RBUTTONUP       = 0x0205
	WM_COMMAND         = 0x0111
	NIM_ADD            = 0x00000000
	NIM_DELETE         = 0x00000002
	NIF_MESSAGE        = 0x00000001
	NIF_ICON           = 0x00000002
	NIF_TIP            = 0x00000004
	IDI_APPLICATION    = 32512
	MOD_CONTROL        = 0x0002
	VK_E               = 0x45
	HOTKEY_ID          = 1
	MF_STRING          = 0x00000000
	MF_SEPARATOR       = 0x00000800
	TPM_BOTTOMALIGN    = 0x0020
	TPM_RIGHTALIGN     = 0x0008
	SM_CXSCREEN        = 0
	IDM_SHOW           = 1001
	IDM_EXIT           = 1002
	SWP_NOACTIVATE    = 0x0010
	SWP_FRAMECHANGED  = 0x0020
	SWP_NOMOVE        = 0x0002
	SWP_NOSIZE        = 0x0001
	SWP_NOZORDER      = 0x0004
	HWND_TOPMOST_VAL  = ^uintptr(0)

	GWL_STYLE        = ^uintptr(15) // -16
	GWL_EXSTYLE      = ^uintptr(19) // -20
	WS_CAPTION       = uintptr(0x00C00000)
	WS_THICKFRAME    = uintptr(0x00040000)
	WS_SYSMENU       = uintptr(0x00080000)
	WS_EX_TOOLWINDOW = uintptr(0x00000080)
)

// ─── Win32 结构体 ─────────────────────────────────────────────
type POINT struct{ X, Y int32 }
type MSG struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}
type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}
type NOTIFYICONDATA struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
}

// ─── 数据模型 ─────────────────────────────────────────────────
type historyEntry struct {
	Path      string `json:"path"`
	Pinned    bool   `json:"pinned"`
	UpdatedAt int64  `json:"updated_at"`
}
type historyStore struct {
	Entries []historyEntry `json:"entries"`
}

// ─── DLL ─────────────────────────────────────────────────────
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procRegisterHotKey      = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey    = user32.NewProc("UnregisterHotKey")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procGetModuleHandleW             = kernel32.NewProc("GetModuleHandleW")
	procShellNotifyIcon              = shell32.NewProc("Shell_NotifyIconW")
	procGetWindowLongPtr             = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr             = user32.NewProc("SetWindowLongPtrW")
	procFindFirstChangeNotification  = kernel32.NewProc("FindFirstChangeNotificationW")
	procFindNextChangeNotification   = kernel32.NewProc("FindNextChangeNotification")
	procFindCloseChangeNotification  = kernel32.NewProc("FindCloseChangeNotification")
	procWaitForSingleObject          = kernel32.NewProc("WaitForSingleObject")
)

// ─── 全局状态 ─────────────────────────────────────────────────
var (
	hwndMessage uintptr
	hInstance   uintptr
	wv          webview.WebView
	mu          sync.Mutex
	visible     bool
	winHWND     uintptr
	toggleCh    = make(chan struct{}, 1)
	quitCh      = make(chan struct{})
	exeDir      string // 解析一次，避免中文路径问题
)

func watchHistory() {
	dir, _ := syscall.UTF16PtrFromString(exeDir)
	const FILE_NOTIFY_CHANGE_LAST_WRITE = 0x00000010
	const WAIT_OBJECT_0 = 0x00000000
	const INFINITE = 0xFFFFFFFF

	h, _, _ := procFindFirstChangeNotification.Call(
		uintptr(unsafe.Pointer(dir)), 0, FILE_NOTIFY_CHANGE_LAST_WRITE)
	if h == 0 || h == ^uintptr(0) {
		return
	}
	defer procFindCloseChangeNotification.Call(h)

	for {
		r, _, _ := procWaitForSingleObject.Call(h, INFINITE)
		if r != WAIT_OBJECT_0 {
			break
		}
		mu.Lock()
		isVisible := visible
		mu.Unlock()
		if isVisible {
			b, _ := json.Marshal(loadSortedEntries())
			wv.Dispatch(func() {
				wv.Eval("window.receiveEntries(" + string(b) + ")")
			})
		}
		procFindNextChangeNotification.Call(h)
	}
}

func getExeDir() string {
	buf := make([]uint16, 32768)
	n, _, _ := procGetModuleHandleW.Call(0)
	getModFileName := kernel32.NewProc("GetModuleFileNameW")
	getModFileName.Call(n, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	full := syscall.UTF16ToString(buf)
	dir := filepath.Dir(full)
	os.WriteFile(filepath.Join(dir, "debug.log"),
		[]byte("getExeDir: "+dir+"\n"), 0644)
	return dir
}

// ─── 入口 ────────────────────────────────────────────────────
func main() {
	runtime.LockOSThread()

	exeDir = getExeDir()

	hInstance, _, _ = procGetModuleHandleW.Call(0)

	// 先初始化 webview，拿到真实 HWND
	initWebview()

	// 托盘
	addTrayIcon()

	// 热键 & 托盘消息：独立 goroutine（自己的 OS 线程）
	go hotKeyLoop()
	go watchHistory()

	// 监听 toggle 请求，派发到主线程（webview.Dispatch）
	go func() {
		for {
			select {
			case <-toggleCh:
				wv.Dispatch(func() {
					mu.Lock()
					defer mu.Unlock()
					if visible {
						procShowWindow.Call(winHWND, SW_HIDE)
						visible = false
					} else {
						repositionWindow()
						procSetForegroundWindow.Call(winHWND)
						procShowWindow.Call(winHWND, SW_SHOW)
						visible = true
						b, _ := json.Marshal(loadSortedEntries())
						wv.Eval("window.receiveEntries(" + string(b) + ")")
					}
				})
			case <-quitCh:
				wv.Terminate()
				return
			}
		}
	}()

	// 阻塞：WebView2 主循环
	wv.Run()

	procUnregisterHotKey.Call(hwndMessage, HOTKEY_ID)
	removeTrayIcon()
}

// hotKeyLoop 在独立 OS 线程处理 WM_HOTKEY 和托盘消息
func hotKeyLoop() {
	runtime.LockOSThread()

	inst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("EasyDesktopHK")
	wc := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     inst,
		LpszClassName: className,
	}
	hcursor, _, _ := procLoadCursorW.Call(0, 32512)
	wc.HCursor = hcursor
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	// HWND_MESSAGE = -3 = ^uintptr(2)
	hwndMessage, _, _ = procCreateWindowEx.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0,
		^uintptr(2), 0, inst, 0,
	)

	procRegisterHotKey.Call(hwndMessage, HOTKEY_ID, MOD_CONTROL, VK_E)

	var msg MSG
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// wndProc 处理热键窗口消息
func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_HOTKEY:
		if wParam == HOTKEY_ID {
			select {
			case toggleCh <- struct{}{}:
			default:
			}
		}
	case WM_TRAYICON:
		switch lParam {
		case WM_LBUTTONUP:
			select {
			case toggleCh <- struct{}{}:
			default:
			}
		case WM_RBUTTONUP:
			showTrayMenu(hwnd)
		}
	case WM_COMMAND:
		switch wParam {
		case IDM_SHOW:
			select {
			case toggleCh <- struct{}{}:
			default:
			}
		case IDM_EXIT:
			removeTrayIcon()
			close(quitCh)
			procPostQuitMessage.Call(0)
		}
	case WM_DESTROY:
		procPostQuitMessage.Call(0)
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

// repositionWindow 每次显示前重新定位（应对分辨率变化）
func repositionWindow() {
	screenW, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	winW := 860
	x := (int(screenW) - winW) / 2
	procSetWindowPos.Call(
		winHWND, HWND_TOPMOST_VAL,
		uintptr(x), 80,
		uintptr(winW), 320,
		SWP_NOACTIVATE,
	)
}

func removeTitleBar(hwnd uintptr) {
	style, _, _ := procGetWindowLongPtr.Call(hwnd, GWL_STYLE)
	style &^= WS_CAPTION | WS_THICKFRAME | WS_SYSMENU
	procSetWindowLongPtr.Call(hwnd, GWL_STYLE, style)
	exStyle, _, _ := procGetWindowLongPtr.Call(hwnd, GWL_EXSTYLE)
	exStyle |= WS_EX_TOOLWINDOW
	procSetWindowLongPtr.Call(hwnd, GWL_EXSTYLE, exStyle)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOZORDER|SWP_FRAMECHANGED)
}

// ─── 托盘图标 ────────────────────────────────────────────────
func addTrayIcon() {
	nid := buildNID()
	procShellNotifyIcon.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
}
func removeTrayIcon() {
	nid := buildNID()
	procShellNotifyIcon.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
}
func buildNID() NOTIFYICONDATA {
	var nid NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwndMessage
	nid.UID = 1
	nid.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.UCallbackMessage = WM_TRAYICON
	hicon, _, _ := procLoadIconW.Call(0, IDI_APPLICATION)
	nid.HIcon = hicon
	tip, _ := syscall.UTF16FromString("EasyDesktop  Ctrl+E 呼出")
	copy(nid.SzTip[:], tip)
	return nid
}
func showTrayMenu(hwnd uintptr) {
	var pt POINT
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	hMenu, _, _ := procCreatePopupMenu.Call()
	showText, _ := syscall.UTF16PtrFromString("显示/隐藏  Ctrl+E")
	exitText, _ := syscall.UTF16PtrFromString("退出")
	procAppendMenu.Call(hMenu, MF_STRING, IDM_SHOW, uintptr(unsafe.Pointer(showText)))
	procAppendMenu.Call(hMenu, MF_SEPARATOR, 0, 0)
	procAppendMenu.Call(hMenu, MF_STRING, IDM_EXIT, uintptr(unsafe.Pointer(exitText)))
	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, TPM_BOTTOMALIGN|TPM_RIGHTALIGN,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procDestroyMenu.Call(hMenu)
}

// ─── WebView 初始化 ──────────────────────────────────────────
func initWebview() {
	screenW, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	winW, winH := 860, 320

	wv = webview.NewWithOptions(webview.WebViewOptions{
		Debug:  true,
		Window: nil,
	})
	wv.SetTitle("EasyDesktop")
	wv.SetSize(winW, winH, webview.HintFixed)

	wv.Bind("goSwitch", func(path string) {
		wv.Dispatch(func() {
			mu.Lock()
			procShowWindow.Call(winHWND, SW_HIDE)
			visible = false
			mu.Unlock()
		})
		cliExe := filepath.Join(exeDir, "easydesktop.exe")
		go exec.Command(cliExe, path).Run()
	})
	wv.Bind("goPin", func(path string) string {
		pinEntry(path)
		b, _ := json.Marshal(loadSortedEntries())
		return string(b)
	})
	wv.Bind("goDelete", func(path string) string {
		deleteEntry(path)
		b, _ := json.Marshal(loadSortedEntries())
		return string(b)
	})
	wv.Bind("goHide", func() {
		wv.Dispatch(func() {
			mu.Lock()
			procShowWindow.Call(winHWND, SW_HIDE)
			visible = false
			mu.Unlock()
		})
	})

	wv.SetHtml(htmlContent())

	winHWND = uintptr(wv.Window())
	removeTitleBar(winHWND)
	x := (int(screenW) - winW) / 2
	procSetWindowPos.Call(
		winHWND, HWND_TOPMOST_VAL,
		uintptr(x), 80,
		uintptr(winW), uintptr(winH),
		SWP_NOACTIVATE,
	)
	procShowWindow.Call(winHWND, SW_HIDE)
}

// ─── 历史数据 ────────────────────────────────────────────────
func historyFile() string {
	return filepath.Join(exeDir, "history.json")
}
func loadSortedEntries() []historyEntry {
	data, err := os.ReadFile(historyFile())
	if err != nil {
		return []historyEntry{}
	}
	var store historyStore
	if json.Unmarshal(data, &store) != nil || store.Entries == nil {
		return []historyEntry{}
	}
	sort.SliceStable(store.Entries, func(i, j int) bool {
		if store.Entries[i].Pinned != store.Entries[j].Pinned {
			return store.Entries[i].Pinned
		}
		return store.Entries[i].UpdatedAt > store.Entries[j].UpdatedAt
	})
	return store.Entries
}
func saveEntries(entries []historyEntry) {
	store := historyStore{Entries: entries}
	b, _ := json.MarshalIndent(store, "", "  ")
	os.WriteFile(historyFile(), b, 0644)
}
func pinEntry(path string) {
	entries := loadSortedEntries()
	for i := range entries {
		if strings.EqualFold(entries[i].Path, path) {
			entries[i].Pinned = !entries[i].Pinned
			entries[i].UpdatedAt = time.Now().Unix()
			break
		}
	}
	saveEntries(entries)
}
func deleteEntry(path string) {
	entries := loadSortedEntries()
	out := entries[:0]
	for _, e := range entries {
		if !strings.EqualFold(e.Path, path) {
			out = append(out, e)
		}
	}
	saveEntries(out)
}

// ─── HTML ────────────────────────────────────────────────────
func htmlContent() string {
	return `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8"/>
<style>
*{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0f0f17;--surface:#16161f;--border:#ffffff0f;--border2:#ffffff18;
  --accent:#818cf8;--accent-bg:#818cf81a;--text:#e2e8f0;--sub:#4a5568;
  --yellow:#f6c90e;--red:#f87171;--r:10px;
}
html,body{width:860px;height:320px;overflow:hidden;background:transparent;
  font-family:'Segoe UI','Microsoft YaHei UI',sans-serif;-webkit-font-smoothing:antialiased;}
#root{width:100%;height:100%;background:var(--bg);border-radius:var(--r);
  border:1px solid var(--border2);
  box-shadow:0 0 0 1px #00000066,0 24px 64px #000000cc,inset 0 1px 0 #ffffff0a;
  display:flex;flex-direction:column;overflow:hidden;-webkit-app-region:drag;}
/* search */
#searchbar{display:flex;align-items:center;gap:8px;padding:10px 14px 8px;flex-shrink:0;-webkit-app-region:drag;}
.sp{display:flex;align-items:center;gap:7px;flex:1;padding:5px 10px;
  background:var(--surface);border:1px solid var(--border2);border-radius:8px;
  -webkit-app-region:no-drag;transition:border-color .15s;}
.sp:focus-within{border-color:rgba(129,140,248,.4);}
.si{color:var(--sub);flex-shrink:0;transition:color .15s;}
.sp:focus-within .si{color:var(--accent);}
#searchInput{flex:1;background:transparent;border:none;outline:none;
  color:var(--text);font-size:13.5px;font-family:inherit;caret-color:var(--accent);
  -webkit-app-region:no-drag;}
#searchInput::placeholder{color:var(--sub);}
#closeBtn{width:24px;height:24px;border-radius:6px;border:none;background:transparent;
  color:var(--sub);cursor:pointer;display:flex;align-items:center;justify-content:center;
  -webkit-app-region:no-drag;flex-shrink:0;transition:background .12s,color .12s;}
#closeBtn:hover{background:#f8717122;color:var(--red);}
/* divider */
.div{height:1px;flex-shrink:0;background:linear-gradient(90deg,transparent,var(--border2) 20%,var(--border2) 80%,transparent);}
/* list */
#listArea{flex:1;overflow:hidden;position:relative;-webkit-app-region:no-drag;}
#listScroll{height:100%;overflow-y:auto;padding:6px 8px;}
#listScroll::-webkit-scrollbar{width:3px;}
#listScroll::-webkit-scrollbar-track{background:transparent;}
#listScroll::-webkit-scrollbar-thumb{background:var(--border2);border-radius:2px;}
/* row */
.row{display:flex;align-items:center;gap:10px;padding:7px 10px;border-radius:8px;
  cursor:pointer;transition:background .12s;position:relative;border:1px solid transparent;}
.row:hover{background:var(--surface);}
.row.active{background:var(--accent-bg);border-color:rgba(129,140,248,.25);}
.row-icon{font-size:16px;flex-shrink:0;line-height:1;}
.row-body{flex:1;min-width:0;}
.row-path{font-size:12px;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;}
.row.active .row-path{color:#c7d2fe;}
.row-meta{display:flex;align-items:center;gap:6px;margin-top:2px;}
.row-drive{font-size:9.5px;color:var(--accent);background:var(--accent-bg);
  border:1px solid rgba(129,140,248,.2);border-radius:3px;padding:0 5px;font-family:monospace;letter-spacing:.03em;}
.row-pin{font-size:9px;color:var(--yellow);}
.row-time{font-size:9.5px;color:var(--sub);font-family:monospace;}
.row-actions{display:flex;gap:4px;flex-shrink:0;opacity:0;transition:opacity .12s;}
.row:hover .row-actions,.row.active .row-actions{opacity:1;}
.rbtn{padding:3px 8px;border-radius:5px;border:1px solid transparent;
  font-size:10px;cursor:pointer;font-family:inherit;transition:background .1s,color .1s,transform .1s;}
.rbtn:active{transform:scale(.94);}
.r-sw{background:var(--accent-bg);border-color:rgba(129,140,248,.2);color:var(--accent);}
.r-sw:hover{background:var(--accent);color:#0f0f17;}
.r-pi{background:#f6c90e12;border-color:#f6c90e22;color:var(--yellow);}
.r-pi:hover{background:var(--yellow);color:#0f0f17;}
.r-de{background:#f8717112;border-color:#f8717122;color:var(--red);}
.r-de:hover{background:var(--red);color:#fff;}
/* status */
#statusbar{padding:5px 14px;display:flex;align-items:center;justify-content:space-between;
  flex-shrink:0;border-top:1px solid var(--border);}
.hint{font-size:9.5px;color:var(--sub);display:flex;align-items:center;gap:5px;}
kbd{background:var(--surface);border:1px solid var(--border2);border-radius:3px;
  padding:1px 4px;font-size:9px;font-family:monospace;color:var(--text);box-shadow:0 1px 0 #00000066;}
.hs{color:#ffffff1a;}
#countLabel{font-size:9.5px;color:var(--sub);font-family:monospace;letter-spacing:.04em;}
/* empty */
#empty{display:none;flex-direction:column;align-items:center;justify-content:center;
  height:100%;color:var(--sub);font-size:12px;gap:8px;}
#empty.show{display:flex;}
</style>
</head>
<body>
<div id="root">
  <div id="searchbar">
    <div class="sp">
      <svg class="si" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2">
        <circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/>
      </svg>
      <input id="searchInput" placeholder="搜索目录路径…" autocomplete="off" spellcheck="false"/>
    </div>
    <button id="closeBtn" title="Esc">
      <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
        <path d="M18 6 6 18M6 6l12 12"/>
      </svg>
    </button>
  </div>
  <div class="div"></div>
  <div id="listArea">
    <div id="listScroll">
      <div id="listTrack"></div>
      <div id="empty"><span style="font-size:28px;opacity:.25">◫</span><span style="opacity:.4">暂无历史记录</span></div>
    </div>
  </div>
  <div id="statusbar">
    <span class="hint">
      <kbd>↑</kbd><kbd>↓</kbd>
      <span class="hs">·</span><kbd>Enter</kbd> 切换
      <span class="hs">·</span><kbd>Ctrl+E</kbd> 呼出
      <span class="hs">·</span><kbd>Esc</kbd> 关闭
    </span>
    <span id="countLabel"></span>
  </div>
</div>
<script>
let allEntries=[],filtered=[],activeIdx=0;
const track=document.getElementById('listTrack');
const searchEl=document.getElementById('searchInput');
const countEl=document.getElementById('countLabel');
const emptyEl=document.getElementById('empty');

window.receiveEntries=function(entries){
  allEntries=entries||[];
  searchEl.value='';
  applyFilter();
  searchEl.focus();
};
function applyFilter(){
  const q=searchEl.value.trim().toLowerCase();
  filtered=q?allEntries.filter(e=>e.path.toLowerCase().includes(q)):[...allEntries];
  activeIdx=0;render();
}
function render(){
  track.innerHTML='';
  const has=filtered.length>0;
  emptyEl.classList.toggle('show',!has);
  if(!has){countEl.textContent='';return;}
  filtered.forEach((e,i)=>{
    const div=document.createElement('div');
    div.className='row'+(i===activeIdx?' active':'');
    const drive=e.path.split(/[\\\/]/)[0]||'';
    const t=e.updated_at?new Date(e.updated_at*1000).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}):'';
    div.innerHTML=
      '<div class="row-icon">📁</div>'+
      '<div class="row-body">'+
        '<div class="row-path">'+esc(e.path)+'</div>'+
        '<div class="row-meta">'+
          (drive?'<span class="row-drive">'+esc(drive)+'</span>':'')+
          (e.pinned?'<span class="row-pin">★ 已置顶</span>':'')+
          (t?'<span class="row-time">'+t+'</span>':'')+
        '</div>'+
      '</div>'+
      '<div class="row-actions">'+
        '<button class="rbtn r-sw">切换</button>'+
        '<button class="rbtn r-pi">'+(e.pinned?'取消':'置顶')+'</button>'+
        '<button class="rbtn r-de">删除</button>'+
      '</div>';
    div.querySelector('.r-sw').onclick=ev=>{ev.stopPropagation();doSwitch(e.path);};
    div.querySelector('.r-pi').onclick=ev=>{ev.stopPropagation();goPin(e.path).then(raw=>{window.receiveEntries(JSON.parse(raw));});};
    div.querySelector('.r-de').onclick=ev=>{ev.stopPropagation();if(confirm('删除？\n'+e.path))goDelete(e.path).then(raw=>{window.receiveEntries(JSON.parse(raw));});};
    div.onclick=()=>setActive(i);
    div.ondblclick=()=>doSwitch(e.path);
    track.appendChild(div);
  });
  countEl.textContent=filtered.length+' 条';
  scrollToActive();
}
function scrollToActive(){
  const rows=track.querySelectorAll('.row');
  if(rows[activeIdx])rows[activeIdx].scrollIntoView({block:'nearest'});
}
function setActive(i){activeIdx=i;render();}
function doSwitch(path){goSwitch(path);}
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'){goHide();return;}
  if(e.key==='ArrowUp'){e.preventDefault();if(activeIdx>0){activeIdx--;render();}return;}
  if(e.key==='ArrowDown'){e.preventDefault();if(activeIdx<filtered.length-1){activeIdx++;render();}return;}
  if(e.key==='Enter'&&filtered[activeIdx]){doSwitch(filtered[activeIdx].path);return;}
  if((e.ctrlKey||e.metaKey)&&e.key==='p'&&filtered[activeIdx]){e.preventDefault();goPin(filtered[activeIdx].path).then(raw=>{window.receiveEntries(JSON.parse(raw));});}
});
searchEl.addEventListener('input',applyFilter);
document.getElementById('closeBtn').onclick=()=>goHide();
function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');}
</script>
</body>
</html>`
}
