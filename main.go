package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ── 虚拟键码 ─────────────────────────────────────────────────
const (
	vkBack   = 0x08
	vkUp     = 0x26
	vkDown   = 0x28
	vkReturn = 0x0D
	vkEscape = 0x1B
	vkDelete = 0x2E
	vkSpace  = 0x20

	ctrlPressed      = 0x0008
	rightCtrlPressed = 0x0004
)

// ── Windows 控制台结构 ────────────────────────────────────────
type keyEventRecord struct {
	BKeyDown          int32
	WRepeatCount      uint16
	WVirtualKeyCode   uint16
	WVirtualScanCode  uint16
	UnicodeChar       uint16
	DwControlKeyState uint32
}

type inputRecord struct {
	EventType uint16
	_         uint16
	KeyEvent  keyEventRecord
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// ── 数据模型 ──────────────────────────────────────────────────
type historyEntry struct {
	Path      string `json:"path"`
	Pinned    bool   `json:"pinned"`
	UpdatedAt int64  `json:"updated_at"`
}

type historyStore struct {
	Entries []historyEntry `json:"entries"`
}

var folderIDDesktop = guid{
	Data1: 0xB4BFCC3A,
	Data2: 0xDB2C,
	Data3: 0x424C,
	Data4: [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41},
}

// ── ANSI 颜色 ─────────────────────────────────────────────────
const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiBlue      = "\x1b[38;5;75m"
	ansiBlueBold  = "\x1b[1;38;5;75m"
	ansiGreen     = "\x1b[38;5;114m"
	ansiYellow    = "\x1b[38;5;221m"
	ansiRed       = "\x1b[38;5;203m"
	ansiGray      = "\x1b[38;5;240m"
	ansiHintKey   = "\x1b[38;5;248m"
	ansiHintDot   = "\x1b[38;5;238m"
	ansiTitle     = "\x1b[38;5;252m"
	ansiSearchBox = "\x1b[38;5;243m"
	ansiPrompt    = "\x1b[38;5;243m"
	ansiQueryText = "\x1b[38;5;255m"
	ansiPinned    = "\x1b[38;5;221m"
	ansiStatusBg  = "\x1b[48;5;25m"
	ansiStatusFg  = "\x1b[38;5;255m"

	innerW = 100
)

// ── 交互模式 ──────────────────────────────────────────────────
type pickerMode int

const (
	modeNormal pickerMode = iota
	modeRename
	modePreview
	modeConfirmDelete
)

// ═══════════════════════════════════════════════════════════════
//  main
// ═══════════════════════════════════════════════════════════════

func main() {
	if len(os.Args) == 1 {
		if err := launchGUI(); err != nil {
			fmt.Fprintf(os.Stderr, "启动图形界面失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--gui", "--ui":
			if err := launchGUI(); err != nil {
				fmt.Fprintf(os.Stderr, "启动图形界面失败: %v\n", err)
				os.Exit(1)
			}
			return
		case "-r", "--recent", "--list":
			if len(os.Args) > 2 {
				if err := switchFromHistory(os.Args[2]); err != nil {
					fmt.Fprintf(os.Stderr, "从历史切换失败: %v\n", err)
					os.Exit(1)
				}
				return
			}
			if err := runRecentPicker(); err != nil {
				fmt.Fprintf(os.Stderr, "历史模式失败: %v\n", err)
				os.Exit(1)
			}
			return
		case "--pin":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "用法: easydesktop --pin <路径|序号>")
				os.Exit(1)
			}
			if err := pinHistory(os.Args[2]); err != nil {
				fmt.Fprintf(os.Stderr, "置顶失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("置顶成功")
			return
		case "--remove", "--rm":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "用法: easydesktop --remove <路径|序号>")
				os.Exit(1)
			}
			if err := removeHistory(os.Args[2]); err != nil {
				fmt.Fprintf(os.Stderr, "删除失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("删除成功")
			return
		case "--move":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "用法: easydesktop --move <fromIndex> <toIndex>")
				os.Exit(1)
			}
			if err := moveHistory(os.Args[2], os.Args[3]); err != nil {
				fmt.Fprintf(os.Stderr, "调整顺序失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("调整顺序成功")
			return
		case "-h", "--help":
			printHelp()
			return
		}
	}

	target := os.Args[1]

	if err := switchDesktop(target, true); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("用法:")
	fmt.Println("  easydesktop                 打开图形界面")
	fmt.Println("  easydesktop [路径]          切换桌面到目标路径")
	fmt.Println("  easydesktop --gui           打开图形界面")
	fmt.Println("  easydesktop -r [序号]       交互选择历史，或按序号直接切换")
	fmt.Println("  easydesktop --pin <参数>    置顶/取消置顶（路径或序号）")
	fmt.Println("  easydesktop --remove <参数> 删除历史（路径或序号）")
	fmt.Println("  easydesktop --move a b      调整历史顺序（序号从1开始）")
	fmt.Println()
	fmt.Println("交互模式快捷键 (-r):")
	fmt.Println("  ↑/↓       导航")
	fmt.Println("  Enter     切换到选中目录")
	fmt.Println("  Space     预览目录内容（再按退出预览）")
	fmt.Println("  Ctrl+R    重命名记录")
	fmt.Println("  Ctrl+P    置顶/取消置顶")
	fmt.Println("  Ctrl+D    删除记录")
	fmt.Println("  Ctrl+A    显示全部/清除搜索")
	fmt.Println("  Esc       退出")
}

func launchGUI() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位当前程序: %w", err)
	}

	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "easydesktop-ui.exe"),
		filepath.Join(exeDir, "dist", "easydesktop-ui.exe"),
	}

	var guiPath string
	for _, candidate := range candidates {
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			guiPath = candidate
			break
		}
	}

	if guiPath == "" {
		return fmt.Errorf("未找到图形界面程序，已检查: %s", strings.Join(candidates, ", "))
	}

	cmd := exec.Command(guiPath)
	cmd.Dir = filepath.Dir(guiPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════
//  历史存储
// ═══════════════════════════════════════════════════════════════

func historyFilePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前程序: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "history.json"), nil
}

func loadHistory() (historyStore, error) {
	file, err := historyFilePath()
	if err != nil {
		return historyStore{}, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return historyStore{Entries: []historyEntry{}}, nil
		}
		return historyStore{}, err
	}
	if len(data) == 0 {
		return historyStore{Entries: []historyEntry{}}, nil
	}
	var store historyStore
	if err := json.Unmarshal(data, &store); err != nil {
		return historyStore{}, err
	}
	if store.Entries == nil {
		store.Entries = []historyEntry{}
	}
	return store, nil
}

func saveHistory(store historyStore) error {
	file, err := historyFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func normalizePathForCompare(p string) string {
	clean := filepath.Clean(p)
	clean = strings.TrimRight(clean, `\\/`)
	return strings.ToLower(clean)
}

func addHistory(path string) error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	norm := normalizePathForCompare(abs)
	keep := make([]historyEntry, 0, len(store.Entries)+1)
	pinned := false
	for _, e := range store.Entries {
		if normalizePathForCompare(e.Path) == norm {
			if e.Pinned {
				pinned = true
			}
			continue
		}
		keep = append(keep, e)
	}
	keep = append(keep, historyEntry{Path: abs, Pinned: pinned, UpdatedAt: time.Now().Unix()})
	store.Entries = keep
	sortHistory(&store)
	return saveHistory(store)
}

func sortHistory(store *historyStore) {
	sort.SliceStable(store.Entries, func(i, j int) bool {
		if store.Entries[i].Pinned != store.Entries[j].Pinned {
			return store.Entries[i].Pinned && !store.Entries[j].Pinned
		}
		return store.Entries[i].UpdatedAt > store.Entries[j].UpdatedAt
	})
}

func filterEntries(entries []historyEntry, query string) []historyEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]historyEntry, len(entries))
		copy(out, entries)
		return out
	}
	out := make([]historyEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Path), q) {
			out = append(out, e)
		}
	}
	return out
}

// ═══════════════════════════════════════════════════════════════
//  交互选择器主循环
// ═══════════════════════════════════════════════════════════════

func runRecentPicker() error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	sortHistory(&store)
	if len(store.Entries) == 0 {
		fmt.Println("暂无历史记录，请先用 easydesktop <路径> 切换一次桌面。")
		return nil
	}

	query       := ""
	selected    := 0
	mode        := modeNormal
	renameInput := ""
	statusMsg   := ""
	previewLines := []string{}

	filtered := filterEntries(store.Entries, query)

	clamp := func() {
		if len(filtered) == 0 {
			selected = 0
			return
		}
		if selected >= len(filtered) {
			selected = len(filtered) - 1
		}
		if selected < 0 {
			selected = 0
		}
	}

	// filtered[selected].Path → store.Entries 下标
	findInStore := func(path string) int {
		for i, e := range store.Entries {
			if normalizePathForCompare(e.Path) == normalizePathForCompare(path) {
				return i
			}
		}
		return -1
	}

	for {
		clamp()

		// ── 渲染 ──────────────────────────────────────────────
		switch mode {
		case modePreview:
			renderWithPreview(filtered, selected, query, previewLines, statusMsg)
		case modeRename:
			renderRename(filtered, selected, query, renameInput, statusMsg)
		case modeConfirmDelete:
			renderConfirmDelete(filtered, selected, query, statusMsg)
		default:
			renderRecentPicker(filtered, selected, query, statusMsg)
		}
		statusMsg = ""

		// ── 读键 ──────────────────────────────────────────────
		key, text, err := readNavKey()
		if err != nil {
			return err
		}

		// ── 重命名模式 ─────────────────────────────────────────
		if mode == modeRename {
			switch key {
			case "enter":
				newName := strings.TrimSpace(renameInput)
				if newName != "" && len(filtered) > 0 {
					si := findInStore(filtered[selected].Path)
					if si >= 0 {
						store.Entries[si].Path = newName
						if err2 := saveHistory(store); err2 != nil {
							statusMsg = "保存失败: " + err2.Error()
						} else {
							statusMsg = "已重命名"
						}
						filtered = filterEntries(store.Entries, query)
					}
				}
				mode = modeNormal
			case "quit":
				mode = modeNormal
			case "backspace":
				runes := []rune(renameInput)
				if len(runes) > 0 {
					renameInput = string(runes[:len(runes)-1])
				}
			case "char":
				renameInput += text
			}
			continue
		}

		// ── 删除确认模式 ───────────────────────────────────────
		if mode == modeConfirmDelete {
			switch key {
			case "char":
				if text == "y" || text == "Y" {
					if len(filtered) > 0 {
						si := findInStore(filtered[selected].Path)
						if si >= 0 {
							store.Entries = append(store.Entries[:si], store.Entries[si+1:]...)
							if err2 := saveHistory(store); err2 != nil {
								statusMsg = "删除失败: " + err2.Error()
							} else {
								statusMsg = "已删除"
							}
							filtered = filterEntries(store.Entries, query)
							clamp()
						}
					}
				} else {
					statusMsg = "已取消"
				}
				mode = modeNormal
			case "quit":
				statusMsg = "已取消"
				mode = modeNormal
			}
			continue
		}

		// ── 预览模式 ───────────────────────────────────────────
		if mode == modePreview {
			switch key {
			case "up":
				if selected > 0 {
					selected--
					previewLines = buildPreview(filtered, selected)
				}
			case "down":
				if selected < len(filtered)-1 {
					selected++
					previewLines = buildPreview(filtered, selected)
				}
			case "space", "quit":
				mode = modeNormal
			case "enter":
				if len(filtered) > 0 {
					fmt.Print("\x1b[2J\x1b[H")
					return switchDesktop(filtered[selected].Path, true)
				}
			}
			continue
		}

		// ── 普通模式 ───────────────────────────────────────────
		switch key {
		case "up":
			if selected > 0 {
				selected--
			}

		case "down":
			if selected < len(filtered)-1 {
				selected++
			}

		case "enter":
			if len(filtered) == 0 {
				break
			}
			fmt.Print("\x1b[2J\x1b[H")
			return switchDesktop(filtered[selected].Path, true)

		case "quit":
			fmt.Print("\x1b[2J\x1b[H")
			return nil

		case "backspace":
			runes := []rune(query)
			if len(runes) > 0 {
				query = string(runes[:len(runes)-1])
				filtered = filterEntries(store.Entries, query)
				selected = 0
			}

		case "char":
			query += text
			filtered = filterEntries(store.Entries, query)
			selected = 0

		case "space":
			if len(filtered) > 0 {
				if mode == modePreview {
					mode = modeNormal
				} else {
					previewLines = buildPreview(filtered, selected)
					mode = modePreview
				}
			}

		case "ctrl+r":
			if len(filtered) > 0 {
				renameInput = filtered[selected].Path
				mode = modeRename
			}

		case "ctrl+p":
			if len(filtered) > 0 {
				si := findInStore(filtered[selected].Path)
				if si >= 0 {
					store.Entries[si].Pinned = !store.Entries[si].Pinned
					store.Entries[si].UpdatedAt = time.Now().Unix()
					sortHistory(&store)
					if err2 := saveHistory(store); err2 != nil {
						statusMsg = "保存失败: " + err2.Error()
					} else {
						if store.Entries[si].Pinned {
							statusMsg = "已置顶 ★"
						} else {
							statusMsg = "已取消置顶"
						}
					}
					filtered = filterEntries(store.Entries, query)
				}
			}

		case "ctrl+d", "delete":
			if len(filtered) > 0 {
				mode = modeConfirmDelete
			}

		case "ctrl+a":
			query = ""
			filtered = filterEntries(store.Entries, query)
			selected = 0
			statusMsg = "显示全部记录"
		}

		if len(filtered) == 0 {
			filtered = []historyEntry{}
		}
	}
}

// ═══════════════════════════════════════════════════════════════
//  目录预览
// ═══════════════════════════════════════════════════════════════

func buildPreview(entries []historyEntry, idx int) []string {
	if idx < 0 || idx >= len(entries) {
		return nil
	}
	path := entries[idx].Path
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return []string{ansiRed + "  无法读取目录: " + err.Error() + ansiReset}
	}

	lines := []string{}
	dirs, files := 0, 0
	maxShow := 12
	for i, de := range dirEntries {
		if i >= maxShow {
			lines = append(lines, ansiGray+fmt.Sprintf("  ... 还有 %d 个项目", len(dirEntries)-maxShow)+ansiReset)
			break
		}
		if de.IsDir() {
			lines = append(lines, ansiBlue+"  [DIR] "+de.Name()+ansiReset)
			dirs++
		} else {
			lines = append(lines, ansiGray+"  [   ] "+de.Name()+ansiReset)
			files++
		}
	}
	if len(dirEntries) == 0 {
		lines = append(lines, ansiGray+"  (空目录)"+ansiReset)
	}
	summary := ansiGray + fmt.Sprintf("  %d 个文件夹  %d 个文件", dirs, files) + ansiReset
	return append([]string{summary, ""}, lines...)
}

// ═══════════════════════════════════════════════════════════════
//  渲染函数
// ═══════════════════════════════════════════════════════════════

func renderRecentPicker(entries []historyEntry, selected int, query, statusMsg string) {
	fmt.Print("\x1b[2J\x1b[H")
	printHeader(query)
	printList(entries, selected)
	printHintBar()
	printStatusBar(len(entries), statusMsg)
}

func renderWithPreview(entries []historyEntry, selected int, query string, lines []string, statusMsg string) {
	fmt.Print("\x1b[2J\x1b[H")
	printHeader(query)
	printList(entries, selected)
	fmt.Println()
	sep := strings.Repeat("─", innerW-8)
	fmt.Print(ansiSearchBox + "  ┌─ 目录预览 " + sep + "┐" + ansiReset + "\n")
	maxLines := 14
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	for _, l := range shown {
		fmt.Println(l)
	}
	fmt.Print(ansiSearchBox + "  └" + strings.Repeat("─", innerW) + "┘" + ansiReset + "\n")
	printHintBar()
	printStatusBar(len(entries), statusMsg)
}

func renderRename(entries []historyEntry, selected int, query, renameInput, statusMsg string) {
	fmt.Print("\x1b[2J\x1b[H")
	printHeader(query)
	printList(entries, selected)
	fmt.Println()
	boxLine := strings.Repeat("─", innerW)
	header := "─ 重命名（Enter 确认，Esc 取消）"
	padLen := innerW - dispWidth(header) - 1
	if padLen < 0 {
		padLen = 0
	}
	fmt.Print(ansiYellow + "  ┌" + header + strings.Repeat("─", padLen) + "┐" + ansiReset + "\n")
	w := dispWidth(renameInput)
	pad := ""
	if innerW-2-w > 0 {
		pad = strings.Repeat(" ", innerW-2-w)
	}
	fmt.Print(ansiYellow + "  │ " + ansiReset + ansiQueryText + renameInput + ansiReset + pad + ansiYellow + "│" + ansiReset + "\n")
	fmt.Print(ansiYellow + "  └" + boxLine + "┘" + ansiReset + "\n")
	printHintBarRename()
	printStatusBar(len(entries), statusMsg)
}

func renderConfirmDelete(entries []historyEntry, selected int, query, statusMsg string) {
	fmt.Print("\x1b[2J\x1b[H")
	printHeader(query)
	printList(entries, selected)
	fmt.Println()
	path := ""
	if selected >= 0 && selected < len(entries) {
		path = entries[selected].Path
	}
	fmt.Print(ansiRed + ansiBold + "  ! 确认删除此条记录？" + ansiReset + "\n")
	fmt.Print(ansiGray + "    " + path + ansiReset + "\n")
	fmt.Println()
	fmt.Print(ansiRed + "  按 Y 确认删除，其他任意键取消" + ansiReset + "\n")
	printStatusBar(len(entries), statusMsg)
}

// ── 通用子渲染 ────────────────────────────────────────────────

func printHeader(query string) {
	fmt.Print(ansiTitle + "  Resume session" + ansiReset + "\n")
	boxLine := strings.Repeat("─", innerW)
	fmt.Print(ansiSearchBox + "  ╭" + boxLine + "╮" + ansiReset + "\n")
	if strings.TrimSpace(query) == "" {
		pad := strings.Repeat(" ", innerW-2-9)
		fmt.Print(ansiSearchBox+"  │ "+ansiReset+ansiPrompt+"⌕ Search…"+ansiReset+pad+ansiSearchBox+"│"+ansiReset+"\n")
	} else {
		contentW := 2 + dispWidth(query)
		pad := ""
		if innerW-2-contentW > 0 {
			pad = strings.Repeat(" ", innerW-2-contentW)
		}
		fmt.Print(ansiSearchBox+"  │ "+ansiReset+ansiPrompt+"⌕ "+ansiReset+ansiQueryText+query+ansiReset+pad+ansiSearchBox+"│"+ansiReset+"\n")
	}
	fmt.Print(ansiSearchBox + "  ╰" + boxLine + "╯" + ansiReset + "\n")
	fmt.Println()
}

func printList(entries []historyEntry, selected int) {
	if len(entries) == 0 {
		fmt.Print(ansiGray + "  (no results)" + ansiReset + "\n")
		return
	}
	for i, e := range entries {
		pin := ""
		if e.Pinned {
			pin = " " + ansiPinned + "★" + ansiReset
		}
		if i == selected {
			fmt.Print(ansiBlueBold+"❯ "+ansiReset+ansiBlue+ansiBold+e.Path+ansiReset+pin+"\n")
		} else {
			fmt.Print(ansiGray+"  "+e.Path+ansiReset+pin+"\n")
		}
	}
}

func printHintBar() {
	fmt.Println()
	type hint struct{ k, v string }
	hints := []hint{
		{"↑/↓", "导航"},
		{"Enter", "切换"},
		{"Space", "预览"},
		{"Ctrl+R", "重命名"},
		{"Ctrl+P", "置顶"},
		{"Ctrl+D", "删除"},
		{"Ctrl+A", "全部"},
		{"Esc", "退出"},
	}
	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = ansiHintKey + h.k + ansiReset + ansiGray + " " + h.v + ansiReset
	}
	dot := ansiHintDot + " · " + ansiReset
	fmt.Print("  " + strings.Join(parts, dot) + "\n")
}

func printHintBarRename() {
	fmt.Println()
	fmt.Print(ansiGray + "  Enter 确认  ·  Esc 取消  ·  Backspace 删字" + ansiReset + "\n")
}

func printStatusBar(count int, msg string) {
	fmt.Println()
	left := fmt.Sprintf("  ↑/↓ 切换 · Enter 选择并切换桌面   共 %d 条", count)
	right := ""
	if msg != "" {
		right = "   " + msg
	}
	fmt.Print(ansiStatusBg + ansiStatusFg + left + right + ansiReset + "\n")
}

// ═══════════════════════════════════════════════════════════════
//  按键读取（支持 Ctrl 组合键）
// ═══════════════════════════════════════════════════════════════

func readNavKey() (string, string, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getStdHandle := kernel32.NewProc("GetStdHandle")
	readConsoleInput := kernel32.NewProc("ReadConsoleInputW")

	h, _, _ := getStdHandle.Call(uintptr(^uint32(10) + 1))
	if h == 0 || h == ^uintptr(0) {
		return "", "", errors.New("无法获取控制台句柄")
	}

	for {
		var rec inputRecord
		var read uint32
		r1, _, e1 := readConsoleInput.Call(
			h,
			uintptr(unsafe.Pointer(&rec)),
			1,
			uintptr(unsafe.Pointer(&read)),
		)
		if r1 == 0 {
			if e1 != nil && e1.Error() != "The operation completed successfully." {
				return "", "", e1
			}
			return "", "", errors.New("读取按键失败")
		}
		if read == 0 || rec.EventType != 0x0001 || rec.KeyEvent.BKeyDown == 0 {
			continue
		}

		ctrl := rec.KeyEvent.DwControlKeyState&ctrlPressed != 0 ||
			rec.KeyEvent.DwControlKeyState&rightCtrlPressed != 0
		vk := rec.KeyEvent.WVirtualKeyCode

		if ctrl {
			switch vk {
			case 'R':
				return "ctrl+r", "", nil
			case 'P':
				return "ctrl+p", "", nil
			case 'D':
				return "ctrl+d", "", nil
			case 'A':
				return "ctrl+a", "", nil
			case 'B':
				return "ctrl+b", "", nil
			}
		}

		switch vk {
		case vkUp:
			return "up", "", nil
		case vkDown:
			return "down", "", nil
		case vkReturn:
			return "enter", "", nil
		case vkEscape:
			return "quit", "", nil
		case vkBack:
			return "backspace", "", nil
		case vkDelete:
			return "delete", "", nil
		case vkSpace:
			return "space", "", nil
		}

		ch := rune(rec.KeyEvent.UnicodeChar)
		if ch >= 32 && ch != 127 {
			return "char", string(ch), nil
		}
	}
}

// ═══════════════════════════════════════════════════════════════
//  显示宽度（CJK = 2 列）
// ═══════════════════════════════════════════════════════════════

func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115F ||
			r == 0x2E80 || r == 0x303F ||
			(r >= 0x3040 && r <= 0x33FF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x4E00 && r <= 0xA4CF) ||
			(r >= 0xA960 && r <= 0xA97F) ||
			(r >= 0xAC00 && r <= 0xD7FF) ||
			(r >= 0xF900 && r <= 0xFAFF) ||
			(r >= 0xFE10 && r <= 0xFE1F) ||
			(r >= 0xFE30 && r <= 0xFE6F) ||
			(r >= 0xFF00 && r <= 0xFF60) ||
			(r >= 0xFFE0 && r <= 0xFFE6) ||
			(r >= 0x1B000 && r <= 0x1B77F) ||
			(r >= 0x1F300 && r <= 0x1F64F) ||
			(r >= 0x20000 && r <= 0x3FFFD)) {
			w += 2
		} else {
			w += 1
		}
	}
	return w
}

// ═══════════════════════════════════════════════════════════════
//  桌面切换
// ═══════════════════════════════════════════════════════════════

func switchDesktop(target string, writeHistory bool) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析路径失败: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("目录不存在: %s", abs)
	}
	if err := setDesktopUserShell(abs); err != nil {
		return fmt.Errorf("设置注册表失败: %v", err)
	}
	if err := setDesktopShell(abs); err != nil {
		return fmt.Errorf("设置注册表失败: %v", err)
	}
	if err := setKnownFolderPath(abs); err != nil {
		return fmt.Errorf("调用系统接口失败: %v", err)
	}
	notifySystemSettingChange()
	forceRefreshExplorer()
	if writeHistory {
		if err := addHistory(abs); err != nil {
			fmt.Fprintf(os.Stderr, "写入历史失败: %v\n", err)
		}
	}
	fmt.Printf("Desktop 路径已切换为: %s\n", abs)
	fmt.Println("说明: 仅修改路径映射，不会自动迁移现有文件。")
	return nil
}

// ═══════════════════════════════════════════════════════════════
//  命令行历史操作
// ═══════════════════════════════════════════════════════════════

func switchFromHistory(input string) error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	sortHistory(&store)
	if len(store.Entries) == 0 {
		return errors.New("历史为空")
	}
	idx, err := parseIndex(input, len(store.Entries))
	if err != nil {
		return errors.New("序号无效")
	}
	return switchDesktop(store.Entries[idx].Path, true)
}

func pinHistory(input string) error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	if len(store.Entries) == 0 {
		return errors.New("历史为空")
	}
	idx, err := parseIndex(input, len(store.Entries))
	if err != nil {
		idx = findByPath(store.Entries, input)
		if idx < 0 {
			return errors.New("找不到记录")
		}
	}
	store.Entries[idx].Pinned = !store.Entries[idx].Pinned
	store.Entries[idx].UpdatedAt = time.Now().Unix()
	sortHistory(&store)
	return saveHistory(store)
}

func removeHistory(input string) error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	if len(store.Entries) == 0 {
		return errors.New("历史为空")
	}
	idx, err := parseIndex(input, len(store.Entries))
	if err != nil {
		idx = findByPath(store.Entries, input)
		if idx < 0 {
			return errors.New("找不到记录")
		}
	}
	store.Entries = append(store.Entries[:idx], store.Entries[idx+1:]...)
	return saveHistory(store)
}

func moveHistory(fromInput, toInput string) error {
	store, err := loadHistory()
	if err != nil {
		return err
	}
	if len(store.Entries) < 2 {
		return errors.New("历史不足两条，无法调整")
	}
	from, err := parseIndex(fromInput, len(store.Entries))
	if err != nil {
		return errors.New("起始序号无效")
	}
	to, err := parseIndex(toInput, len(store.Entries))
	if err != nil {
		return errors.New("目标序号无效")
	}
	if from == to {
		return nil
	}
	entry := store.Entries[from]
	store.Entries = append(store.Entries[:from], store.Entries[from+1:]...)
	if to > from {
		to--
	}
	store.Entries = append(store.Entries[:to], append([]historyEntry{entry}, store.Entries[to:]...)...)
	entry.UpdatedAt = time.Now().Unix()
	store.Entries[to] = entry
	return saveHistory(store)
}

func parseIndex(input string, max int) (int, error) {
	n, err := strconv.Atoi(input)
	if err != nil {
		return -1, errors.New("不是有效序号")
	}
	if n < 1 || n > max {
		return -1, errors.New("序号超出范围")
	}
	return n - 1, nil
}

func findByPath(entries []historyEntry, input string) int {
	norm := normalizePathForCompare(input)
	for i, e := range entries {
		if normalizePathForCompare(e.Path) == norm {
			return i
		}
	}
	return -1
}

// ═══════════════════════════════════════════════════════════════
//  Windows 注册表 / 系统调用
// ═══════════════════════════════════════════════════════════════

func setDesktopUserShell(path string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`,
		registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetExpandStringValue("Desktop", path)
}

func setDesktopShell(path string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Explorer\Shell Folders`,
		registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("Desktop", path)
}

func setKnownFolderPath(path string) error {
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	proc := shell32.NewProc("SHSetKnownFolderPath")
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	hr, _, _ := proc.Call(
		uintptr(unsafe.Pointer(&folderIDDesktop)),
		0,
		0,
		uintptr(unsafe.Pointer(p)),
	)
	if int32(hr) != 0 {
		return fmt.Errorf("HRESULT=0x%08X", uint32(hr))
	}
	return nil
}

func forceRefreshExplorer() {
	_ = exec.Command("taskkill", "/F", "/IM", "explorer.exe").Run()
	time.Sleep(700 * time.Millisecond)
	_ = exec.Command("cmd", "/C", "start", "", "explorer.exe").Run()
}

func notifySystemSettingChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	section, _ := windows.UTF16PtrFromString("User Shell Folders")
	const (
		hwndBroadcast   = 0xFFFF
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	var result uintptr
	proc.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(section)),
		smtoAbortIfHung,
		2000,
		uintptr(unsafe.Pointer(&result)),
	)
}
