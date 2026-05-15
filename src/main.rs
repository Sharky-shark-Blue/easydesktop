use std::collections::HashSet;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::mpsc::{self, Receiver};
use std::thread;
use std::time::{Duration, Instant};

use chrono::Local;
use eframe::egui::{
    self, Align, Align2, Color32, CornerRadius, FontData, FontDefinitions, FontFamily, FontId,
    Key, Layout, Pos2, Rect, Sense, Stroke, StrokeKind,
};
use serde::{Deserialize, Serialize};

#[cfg(windows)]
use global_hotkey::{
    hotkey::{Code, HotKey, Modifiers},
    GlobalHotKeyEvent, GlobalHotKeyManager, HotKeyState,
};
#[cfg(windows)]
use std::os::windows::ffi::OsStrExt;
#[cfg(windows)]
use winreg::{enums::HKEY_CURRENT_USER, RegKey};

const HOTKEY_LABELS: [&str; 5] = ["Alt+1", "Alt+2", "Alt+3", "Alt+4", "Alt+5"];
const SUMMON_HOTKEY_LABEL: &str = "Ctrl+E";
const MAX_HISTORY: usize = 6;
const WINDOW_WIDTH: f32 = 760.0;
const WINDOW_HEIGHT: f32 = 608.0;
const PALETTE_RADIUS: u8 = 22;
const ROW_HEIGHT: f32 = 82.0;
const ROW_GAP: f32 = 8.0;
const HEADER_HEIGHT: f32 = 92.0;
const FOOTER_HEIGHT: f32 = 58.0;
const STATUS_HEIGHT: f32 = 24.0;
#[cfg(windows)]
const SHCNE_ASSOCCHANGED: i32 = 0x0800_0000;
#[cfg(windows)]
const SHCNE_UPDATEDIR: i32 = 0x0000_1000;
#[cfg(windows)]
const SHCNF_IDLIST: u32 = 0x0000;
#[cfg(windows)]
const SHCNF_PATHW: u32 = 0x0005;
#[cfg(windows)]
const SHCNF_FLUSH: u32 = 0x1000;
#[cfg(windows)]
const HWND_BROADCAST: isize = 0xffff;
#[cfg(windows)]
const WM_SETTINGCHANGE: u32 = 0x001A;
#[cfg(windows)]
const SMTO_ABORTIFHUNG: u32 = 0x0002;

enum AppCommand {
    TogglePalette,
}

#[cfg(windows)]
#[repr(C)]
struct Guid {
    data1: u32,
    data2: u16,
    data3: u16,
    data4: [u8; 8],
}

#[cfg(windows)]
const FOLDERID_DESKTOP: Guid = Guid {
    data1: 0xB4BFCC3A,
    data2: 0xDB2C,
    data3: 0x424C,
    data4: [0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41],
};

#[cfg(windows)]
#[link(name = "shell32")]
unsafe extern "system" {
    fn SHSetKnownFolderPath(
        rfid: *const Guid,
        dw_flags: u32,
        h_token: isize,
        psz_path: *const u16,
    ) -> i32;
    fn SHChangeNotify(event_id: i32, flags: u32, item1: *const std::ffi::c_void, item2: *const std::ffi::c_void);
}

#[cfg(windows)]
#[link(name = "user32")]
unsafe extern "system" {
    fn SendMessageTimeoutW(
        hwnd: isize,
        msg: u32,
        w_param: usize,
        l_param: isize,
        flags: u32,
        timeout: u32,
        result: *mut usize,
    ) -> isize;
}

#[cfg(windows)]
struct HotKeyRegistration {
    _manager: GlobalHotKeyManager,
}

#[cfg(not(windows))]
struct HotKeyRegistration;

fn main() -> eframe::Result<()> {
    if let Some(exit_code) = run_cli_mode() {
        std::process::exit(exit_code);
    }

    let native_options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_inner_size([WINDOW_WIDTH, WINDOW_HEIGHT])
            .with_min_inner_size([WINDOW_WIDTH, WINDOW_HEIGHT])
            .with_max_inner_size([WINDOW_WIDTH, WINDOW_HEIGHT])
            .with_resizable(false)
            .with_transparent(true)
            .with_decorations(false)
            .with_title("EasyDesktop"),
        ..Default::default()
    };

    eframe::run_native(
        "EasyDesktop",
        native_options,
        Box::new(|cc| Ok(Box::new(EasyDesktopApp::new(cc)))),
    )
}

fn run_cli_mode() -> Option<i32> {
    let help_flag = std::ffi::OsStr::new("-h");
    let help_long_flag = std::ffi::OsStr::new("--help");
    let help_windows_flag = std::ffi::OsStr::new("/?");
    let restore_flag = std::ffi::OsStr::new("-r");
    let restore_long_flag = std::ffi::OsStr::new("--restore");

    let mut args = std::env::args_os();
    let _program = args.next();
    let first = args.next()?;

    if args.next().is_some() {
        eprintln!("参数过多。用法: easydesktop [目录路径 | -r]");
        return Some(2);
    }

    if first == help_flag || first == help_long_flag || first == help_windows_flag {
        println!("用法:");
        println!("  easydesktop              打开图形界面");
        println!("  easydesktop <目录路径>   将桌面切换到指定目录");
        println!("  easydesktop -r          恢复默认桌面路径");
        return Some(0);
    }

    if first == restore_flag || first == restore_long_flag {
        let Some(path) = default_desktop_path() else {
            eprintln!("无法确定默认桌面路径");
            return Some(1);
        };
        return Some(apply_cli_switch(path));
    }

    let path = PathBuf::from(first);
    Some(apply_cli_switch(path))
}

fn apply_cli_switch(path: PathBuf) -> i32 {
    let resolved = if path.is_absolute() {
        path
    } else {
        match std::env::current_dir() {
            Ok(current) => current.join(path),
            Err(err) => {
                eprintln!("无法获取当前目录: {err}");
                return 1;
            }
        }
    };

    if !resolved.exists() {
        eprintln!("目录不存在: {}", resolved.display());
        return 1;
    }

    if !resolved.is_dir() {
        eprintln!("不是目录: {}", resolved.display());
        return 1;
    }

    match set_desktop_path(&resolved) {
        Ok(()) => {
            println!("Desktop 路径已切换为: {}", resolved.display());
            println!("说明: 仅修改路径映射，不会自动迁移现有文件。");
            0
        }
        Err(err) => {
            eprintln!("切换失败: {err}");
            1
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum EntryKind {
    Current,
    Saved,
    PickOther,
}

#[derive(Clone)]
struct Entry {
    title: String,
    subtitle: String,
    path: Option<PathBuf>,
    kind: EntryKind,
    pinned: bool,
}

#[derive(Default, Serialize, Deserialize)]
struct SavedState {
    history: Vec<String>,
    pinned: Vec<String>,
}

struct EasyDesktopApp {
    search: String,
    entries: Vec<Entry>,
    filtered: Vec<usize>,
    selected_visible: usize,
    state_path: PathBuf,
    state: SavedState,
    status: String,
    clock_text: String,
    last_clock_tick: Instant,
    search_focus_pending: bool,
    command_rx: Receiver<AppCommand>,
    _hotkey_registration: Option<HotKeyRegistration>,
    window_visible: bool,
}

impl EasyDesktopApp {
    fn new(cc: &eframe::CreationContext<'_>) -> Self {
        configure_fonts(&cc.egui_ctx);
        configure_style(&cc.egui_ctx);

        let state_path = app_state_path();
        let state = load_state(&state_path);
        let desktop_path = read_desktop_path().unwrap_or_else(|_| default_desktop_path());
        let (command_rx, hotkey_registration) = setup_global_hotkey(&cc.egui_ctx);

        let mut entries = Vec::new();
        entries.push(Entry {
            title: "当前目录".to_owned(),
            subtitle: "立即将 Desktop 切换到当前工作目录".to_owned(),
            path: std::env::current_dir().ok(),
            kind: EntryKind::Current,
            pinned: true,
        });

        for path in dedup_paths(&state.history) {
            if Some(path.clone()) == desktop_path {
                continue;
            }
            entries.push(Entry {
                title: path.display().to_string(),
                subtitle: "历史桌面目录".to_owned(),
                path: Some(path),
                kind: EntryKind::Saved,
                pinned: false,
            });
        }

        if let Some(path) = desktop_path {
            if !entries
                .iter()
                .any(|entry| entry.path.as_ref().map(|p| same_path(p, &path)).unwrap_or(false))
            {
                entries.push(Entry {
                    title: path.display().to_string(),
                    subtitle: "当前系统 Desktop 路径".to_owned(),
                    path: Some(path),
                    kind: EntryKind::Saved,
                    pinned: false,
                });
            }
        }

        entries.push(Entry {
            title: "选择其他目录…".to_owned(),
            subtitle: "浏览文件夹并切换，不迁移已有文件".to_owned(),
            path: None,
            kind: EntryKind::PickOther,
            pinned: false,
        });

        let pinned_set: HashSet<&str> = state.pinned.iter().map(String::as_str).collect();
        for entry in &mut entries {
            if let Some(path) = &entry.path {
                entry.pinned |= pinned_set.contains(path.to_string_lossy().as_ref());
            }
        }

        let mut app = Self {
            search: String::new(),
            entries,
            filtered: Vec::new(),
            selected_visible: 0,
            state_path,
            state,
            status: if hotkey_registration.is_some() {
                format!("全局唤出快捷键：{SUMMON_HOTKEY_LABEL}")
            } else {
                "全局唤出快捷键注册失败".to_owned()
            },
            clock_text: current_clock_text(),
            last_clock_tick: Instant::now(),
            search_focus_pending: true,
            command_rx,
            _hotkey_registration: hotkey_registration,
            window_visible: true,
        };
        app.sort_entries();
        app.rebuild_filtered();
        app
    }

    fn rebuild_filtered(&mut self) {
        let query = self.search.trim().to_lowercase();
        self.filtered = self
            .entries
            .iter()
            .enumerate()
            .filter_map(|(idx, entry)| {
                if query.is_empty() {
                    return Some(idx);
                }

                let title = entry.title.to_lowercase();
                let path = entry
                    .path
                    .as_ref()
                    .map(|p| p.to_string_lossy().to_lowercase())
                    .unwrap_or_default();

                (title.contains(&query) || path.contains(&query)).then_some(idx)
            })
            .take(HOTKEY_LABELS.len())
            .collect();

        if self.filtered.is_empty() {
            self.selected_visible = 0;
        } else if self.selected_visible >= self.filtered.len() {
            self.selected_visible = self.filtered.len() - 1;
        }
    }

    fn selected_entry_index(&self) -> Option<usize> {
        self.filtered.get(self.selected_visible).copied()
    }

    fn selected_entry_mut(&mut self) -> Option<&mut Entry> {
        let idx = self.selected_entry_index()?;
        self.entries.get_mut(idx)
    }

    fn update_clock(&mut self) {
        if self.last_clock_tick.elapsed() >= Duration::from_secs(1) {
            self.clock_text = current_clock_text();
            self.last_clock_tick = Instant::now();
        }
    }

    fn process_app_commands(&mut self, ctx: &egui::Context) {
        while let Ok(command) = self.command_rx.try_recv() {
            match command {
                AppCommand::TogglePalette => {
                    if self.window_visible {
                        self.hide_window(ctx);
                    } else {
                        self.show_window(ctx);
                    }
                }
            }
        }
    }

    fn handle_shortcuts(&mut self, ctx: &egui::Context) {
        if ctx.input(|i| i.key_pressed(Key::ArrowDown)) && !self.filtered.is_empty() {
            self.selected_visible = (self.selected_visible + 1) % self.filtered.len();
        }

        if ctx.input(|i| i.key_pressed(Key::ArrowUp)) && !self.filtered.is_empty() {
            if self.selected_visible == 0 {
                self.selected_visible = self.filtered.len() - 1;
            } else {
                self.selected_visible -= 1;
            }
        }

        for (idx, key) in [Key::Num1, Key::Num2, Key::Num3, Key::Num4, Key::Num5]
            .iter()
            .enumerate()
        {
            if ctx.input(|i| i.modifiers.alt && i.key_pressed(*key)) && idx < self.filtered.len() {
                self.selected_visible = idx;
                self.activate_selected();
            }
        }

        if ctx.input(|i| i.key_pressed(Key::Enter)) {
            self.activate_selected();
        }

        if ctx.input(|i| i.modifiers.ctrl && i.key_pressed(Key::P)) {
            self.toggle_pin_selected();
        }

        if ctx.input(|i| i.modifiers.ctrl && i.key_pressed(Key::D)) {
            self.delete_selected_history();
        }

        if ctx.input(|i| i.key_pressed(Key::Escape)) {
            self.hide_window(ctx);
        }
    }

    fn activate_selected(&mut self) {
        let Some(idx) = self.selected_entry_index() else {
            return;
        };

        match self.entries[idx].kind {
            EntryKind::PickOther => {
                if let Some(path) = rfd::FileDialog::new().pick_folder() {
                    self.switch_to(path);
                }
            }
            EntryKind::Current | EntryKind::Saved => {
                if let Some(path) = self.entries[idx].path.clone() {
                    self.switch_to(path);
                }
            }
        }
    }

    fn switch_to(&mut self, path: PathBuf) {
        match set_desktop_path(&path) {
            Ok(()) => {
                self.push_history(path.clone());
                self.status = format!("Desktop 路径已切换为: {}", path.display());
            }
            Err(err) => {
                self.status = format!("切换失败: {err}");
            }
        }
    }

    fn push_history(&mut self, path: PathBuf) {
        let path_string = path.to_string_lossy().to_string();
        self.state
            .history
            .retain(|existing| !existing.eq_ignore_ascii_case(&path_string));
        self.state.history.insert(0, path_string.clone());
        self.state.history.truncate(MAX_HISTORY);

        if !self
            .entries
            .iter()
            .any(|entry| entry.path.as_ref().map(|p| same_path(p, &path)).unwrap_or(false))
        {
            let pinned = self
                .state
                .pinned
                .iter()
                .any(|saved| saved.eq_ignore_ascii_case(&path_string));

            self.entries.push(Entry {
                title: path.display().to_string(),
                subtitle: "最近切换的桌面目录".to_owned(),
                path: Some(path),
                kind: EntryKind::Saved,
                pinned,
            });
        }

        let _ = save_state(&self.state_path, &self.state);
        self.sort_entries();
        self.rebuild_filtered();
    }

    fn sort_entries(&mut self) {
        let default_path = default_desktop_path();
        let history = self.state.history.clone();

        let mut current = None;
        let mut pick_other = None;
        let mut saved = Vec::new();

        for entry in self.entries.drain(..) {
            match entry.kind {
                EntryKind::Current => current = Some(entry),
                EntryKind::PickOther => pick_other = Some(entry),
                EntryKind::Saved => saved.push(entry),
            }
        }

        saved.sort_by(|a, b| {
            let a_path = a.path.as_deref();
            let b_path = b.path.as_deref();
            let a_default = a_path
                .and_then(|path| default_path.as_ref().map(|default| same_path(path, default)))
                .unwrap_or(false);
            let b_default = b_path
                .and_then(|path| default_path.as_ref().map(|default| same_path(path, default)))
                .unwrap_or(false);
            let a_rank = a_path
                .map(|path| history_rank(&history, path))
                .unwrap_or(usize::MAX);
            let b_rank = b_path
                .map(|path| history_rank(&history, path))
                .unwrap_or(usize::MAX);

            b.pinned
                .cmp(&a.pinned)
                .then_with(|| a_default.cmp(&b_default))
                .then_with(|| a_rank.cmp(&b_rank))
                .then_with(|| a.title.cmp(&b.title))
        });

        let mut rebuilt = Vec::new();
        if let Some(entry) = current {
            rebuilt.push(entry);
        }
        rebuilt.extend(saved);
        if let Some(entry) = pick_other {
            rebuilt.push(entry);
        }

        self.entries = rebuilt;
    }

    fn toggle_pin_selected(&mut self) {
        let mut changed_path = None;
        if let Some(entry) = self.selected_entry_mut() {
            if entry.kind == EntryKind::Saved {
                entry.pinned = !entry.pinned;
                if let Some(path) = &entry.path {
                    changed_path = Some(path.to_string_lossy().to_string());
                }
            }
        }

        if let Some(path) = changed_path {
            if self
                .state
                .pinned
                .iter()
                .any(|saved| saved.eq_ignore_ascii_case(&path))
            {
                self.state
                    .pinned
                    .retain(|saved| !saved.eq_ignore_ascii_case(&path));
                self.status = "已取消置顶".to_owned();
            } else {
                self.state.pinned.push(path);
                self.status = "已置顶".to_owned();
            }
            let _ = save_state(&self.state_path, &self.state);
            self.sort_entries();
            self.rebuild_filtered();
        }
    }

    fn delete_selected_history(&mut self) {
        let Some(idx) = self.selected_entry_index() else {
            return;
        };
        if self.entries[idx].kind != EntryKind::Saved {
            return;
        }

        let Some(path) = self.entries[idx].path.clone() else {
            return;
        };

        let key = path.to_string_lossy().to_string();
        self.state
            .history
            .retain(|saved| !saved.eq_ignore_ascii_case(&key));
        self.state
            .pinned
            .retain(|saved| !saved.eq_ignore_ascii_case(&key));

        self.entries.remove(idx);
        self.status = "已删除历史记录".to_owned();
        let _ = save_state(&self.state_path, &self.state);
        self.rebuild_filtered();
    }

    fn show_window(&mut self, ctx: &egui::Context) {
        self.window_visible = true;
        self.search.clear();
        self.rebuild_filtered();
        self.search_focus_pending = true;
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(true));
        ctx.send_viewport_cmd(egui::ViewportCommand::Minimized(false));
        if let Some(monitor) = ctx.input(|i| i.viewport().monitor_size) {
            let x = (monitor.x - WINDOW_WIDTH) / 2.0;
            let y = (monitor.y - WINDOW_HEIGHT) / 2.0;
            ctx.send_viewport_cmd(egui::ViewportCommand::OuterPosition(
                egui::pos2(x.max(0.0), y.max(0.0)),
            ));
        }
        ctx.send_viewport_cmd(egui::ViewportCommand::Focus);
        ctx.request_repaint();
    }

    fn hide_window(&mut self, ctx: &egui::Context) {
        self.window_visible = false;
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(false));
    }

    fn draw_background(&self, ui: &mut egui::Ui, rect: Rect) {
        let painter = ui.painter();
        painter.rect_filled(rect, 0.0, Color32::from_rgb(7, 11, 18));

        painter.circle_filled(
            rect.left_top() + egui::vec2(rect.width() * 0.16, rect.height() * 0.18),
            rect.width() * 0.32,
            Color32::from_rgba_premultiplied(11, 17, 26, 170),
        );
        painter.circle_filled(
            rect.right_bottom() - egui::vec2(rect.width() * 0.18, rect.height() * 0.22),
            rect.width() * 0.38,
            Color32::from_rgba_premultiplied(16, 24, 32, 128),
        );
        painter.circle_filled(
            rect.center() + egui::vec2(0.0, -rect.height() * 0.10),
            rect.width() * 0.18,
            Color32::from_rgba_premultiplied(16, 22, 30, 82),
        );
    }

    fn draw_palette(&self, ui: &mut egui::Ui, rect: Rect) {
        let painter = ui.painter();

        for (expand, alpha) in [(38.0, 12), (24.0, 20), (14.0, 30), (6.0, 42)] {
            painter.rect_filled(
                rect.expand(expand),
                CornerRadius::same(PALETTE_RADIUS),
                Color32::from_rgba_premultiplied(0, 0, 0, alpha),
            );
        }

        painter.rect_filled(
            rect,
            CornerRadius::same(PALETTE_RADIUS),
            Color32::from_rgba_premultiplied(22, 26, 32, 220),
        );
        painter.rect_stroke(
            rect,
            CornerRadius::same(PALETTE_RADIUS),
            Stroke::new(1.0, Color32::from_rgba_premultiplied(255, 255, 255, 38)),
            StrokeKind::Outside,
        );

        let top_glow = Rect::from_min_size(rect.min, egui::vec2(rect.width(), rect.height() * 0.18));
        painter.rect_filled(
            top_glow,
            CornerRadius::same(PALETTE_RADIUS),
            Color32::from_rgba_premultiplied(255, 255, 255, 10),
        );
    }

    fn draw_header(&mut self, ui: &mut egui::Ui, rect: Rect) -> bool {
        let painter = ui.painter();
        let shell_rect = rect.shrink2(egui::vec2(8.0, 16.0));

        painter.rect_filled(
            shell_rect,
            CornerRadius::same(16),
            Color32::from_rgba_premultiplied(255, 255, 255, 8),
        );
        painter.rect_stroke(
            shell_rect,
            CornerRadius::same(16),
            Stroke::new(1.0, Color32::from_rgba_premultiplied(255, 255, 255, 14)),
            StrokeKind::Outside,
        );
        draw_divider(
            painter,
            rect.left(),
            rect.right(),
            rect.bottom() - 0.5,
            Color32::from_rgba_premultiplied(255, 255, 255, 18),
        );

        let mut close_clicked = false;
        ui.scope_builder(egui::UiBuilder::new().max_rect(shell_rect), |ui| {
            ui.with_layout(Layout::left_to_right(Align::Center), |ui| {
                let text_width = (ui.available_width() - 154.0).max(180.0);

                ui.scope(|ui| {
                    let visuals = ui.visuals_mut();
                    visuals.widgets.inactive.bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.hovered.bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.active.bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.inactive.weak_bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.hovered.weak_bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.active.weak_bg_fill = Color32::TRANSPARENT;
                    visuals.widgets.inactive.bg_stroke = Stroke::NONE;
                    visuals.widgets.hovered.bg_stroke = Stroke::NONE;
                    visuals.widgets.active.bg_stroke = Stroke::NONE;
                    visuals.override_text_color = Some(Color32::from_rgb(245, 245, 245));
                    visuals.selection.bg_fill = Color32::from_rgba_premultiplied(34, 167, 255, 90);

                    let response = ui.add_sized(
                        [text_width, 54.0],
                        egui::TextEdit::singleline(&mut self.search)
                            .hint_text("在此处输入以搜索桌面目录")
                            .font(FontId::new(20.0, FontFamily::Proportional))
                            .margin(egui::vec2(14.0, 15.0)),
                    );

                    if self.search_focus_pending {
                        response.request_focus();
                        self.search_focus_pending = false;
                    }

                    if response.changed() {
                        self.rebuild_filtered();
                    }
                });

                ui.add_space(10.0);

                let (info_rect, _) = ui.allocate_exact_size(egui::vec2(94.0, 54.0), Sense::hover());
                ui.painter().text(
                    Pos2::new(info_rect.left() + 2.0, info_rect.center().y),
                    Align2::LEFT_CENTER,
                    &self.clock_text,
                    FontId::new(18.0, FontFamily::Proportional),
                    Color32::from_rgb(203, 213, 225),
                );
                draw_search_icon(
                    ui.painter(),
                    info_rect.right_center() - egui::vec2(16.0, 0.0),
                    8.5,
                    Color32::from_rgb(203, 213, 225),
                    1.8,
                );

                let close_rect = Rect::from_center_size(
                    Pos2::new(info_rect.right() + 24.0, info_rect.center().y),
                    egui::vec2(30.0, 30.0),
                );
                let close_response = ui.allocate_rect(close_rect, Sense::click());
                let close_bg = if close_response.hovered() {
                    Color32::from_rgba_premultiplied(255, 255, 255, 18)
                } else {
                    Color32::from_rgba_premultiplied(255, 255, 255, 8)
                };
                ui.painter()
                    .circle_filled(close_rect.center(), 15.0, close_bg);
                draw_close_icon(
                    ui.painter(),
                    close_rect.center(),
                    7.0,
                    Color32::from_rgb(203, 213, 225),
                    1.6,
                );
                close_clicked = close_response.clicked();
            });
        });

        close_clicked
    }

    fn draw_list(&mut self, ui: &mut egui::Ui, rect: Rect) {
        let panel_rect = rect.shrink2(egui::vec2(8.0, 2.0));
        ui.painter().rect_filled(
            panel_rect,
            CornerRadius::same(18),
            Color32::from_rgba_premultiplied(255, 255, 255, 5),
        );
        ui.painter().rect_stroke(
            panel_rect,
            CornerRadius::same(18),
            Stroke::new(1.0, Color32::from_rgba_premultiplied(255, 255, 255, 12)),
            StrokeKind::Outside,
        );

        let visible = self.filtered.clone();
        if visible.is_empty() {
            ui.painter().text(
                panel_rect.center(),
                Align2::CENTER_CENTER,
                "没有匹配目录",
                FontId::new(16.0, FontFamily::Proportional),
                Color32::from_rgb(139, 148, 158),
            );
            return;
        }

        let block_height =
            visible.len() as f32 * ROW_HEIGHT + (visible.len().saturating_sub(1) as f32 * ROW_GAP);
        let list_bounds = panel_rect.shrink2(egui::vec2(10.0, 12.0));
        let block_rect = Rect::from_center_size(
            list_bounds.center(),
            egui::vec2(list_bounds.width(), block_height.min(list_bounds.height())),
        );

        ui.scope_builder(egui::UiBuilder::new().max_rect(block_rect), |ui| {
            for (visible_idx, &entry_idx) in visible.iter().enumerate() {
                let selected = visible_idx == self.selected_visible;
                let hotkey = HOTKEY_LABELS.get(visible_idx).copied();
                let entry = self.entries[entry_idx].clone();
                let subtitle = self.subtitle_for(&entry);
                let row = self.draw_row(ui, &entry, &subtitle, selected, hotkey);

                if row.clicked() {
                    self.selected_visible = visible_idx;
                    self.activate_selected();
                } else if row.hovered() {
                    self.selected_visible = visible_idx;
                }

                if visible_idx + 1 < visible.len() {
                    ui.add_space(ROW_GAP);
                }
            }
        });
    }

    fn draw_row(
        &self,
        ui: &mut egui::Ui,
        entry: &Entry,
        subtitle: &str,
        selected: bool,
        hotkey: Option<&str>,
    ) -> egui::Response {
        let (rect, response) =
            ui.allocate_exact_size(egui::vec2(ui.available_width(), ROW_HEIGHT), Sense::click());
        let painter = ui.painter_at(rect);
        let inner = rect.shrink2(egui::vec2(4.0, 2.0));

        if selected || response.hovered() {
            let bg = if selected {
                Color32::from_rgba_premultiplied(255, 255, 255, 19)
            } else {
                Color32::from_rgba_premultiplied(255, 255, 255, 10)
            };
            painter.rect_filled(inner, CornerRadius::same(14), bg);
        }

        if selected {
            let accent = Rect::from_min_size(
                Pos2::new(inner.left() + 2.0, inner.top() + 11.0),
                egui::vec2(6.0, inner.height() - 22.0),
            );
            painter.rect_filled(
                accent,
                CornerRadius::same(4),
                Color32::from_rgb(34, 167, 255),
            );
        }

        let icon_rect = Rect::from_min_size(
            Pos2::new(inner.left() + 18.0, inner.center().y - 19.0),
            egui::vec2(38.0, 38.0),
        );
        match entry.kind {
            EntryKind::Current => draw_star_icon(
                &painter,
                icon_rect.center(),
                18.5,
                Color32::from_rgb(255, 205, 47),
            ),
            EntryKind::Saved | EntryKind::PickOther => draw_folder_search_icon(
                &painter,
                icon_rect,
                Color32::from_rgb(71, 214, 255),
                Color32::from_rgba_premultiplied(7, 11, 18, 160),
            ),
        }

        let text_center_x = (inner.left() + 86.0 + inner.right() - 90.0) / 2.0;
        painter.text(
            Pos2::new(text_center_x, inner.top() + 22.0),
            Align2::CENTER_CENTER,
            &entry.title,
            FontId::new(20.0, FontFamily::Proportional),
            Color32::from_rgb(244, 244, 245),
        );
        painter.text(
            Pos2::new(text_center_x, inner.top() + 50.0),
            Align2::CENTER_CENTER,
            subtitle,
            FontId::new(15.0, FontFamily::Proportional),
            Color32::from_rgb(156, 163, 175),
        );

        if let Some(label) = hotkey {
            let badge_rect = Rect::from_center_size(
                Pos2::new(inner.right() - 52.0, inner.center().y),
                egui::vec2(74.0, 32.0),
            );
            painter.rect_filled(
                badge_rect,
                CornerRadius::same(8),
                Color32::from_rgba_premultiplied(255, 255, 255, 18),
            );
            painter.rect_stroke(
                badge_rect,
                CornerRadius::same(8),
                Stroke::new(1.0, Color32::from_rgba_premultiplied(255, 255, 255, 18)),
                StrokeKind::Outside,
            );
            painter.text(
                badge_rect.center(),
                Align2::CENTER_CENTER,
                label,
                FontId::new(14.0, FontFamily::Proportional),
                Color32::from_rgb(203, 213, 225),
            );
        }

        response
    }

    fn draw_footer(&self, ui: &mut egui::Ui, rect: Rect) {
        draw_divider(
            ui.painter(),
            rect.left(),
            rect.right(),
            rect.top() + 0.5,
            Color32::from_rgba_premultiplied(255, 255, 255, 18),
        );

        ui.painter().text(
            rect.center(),
            Align2::CENTER_CENTER,
            "Enter 切换  ·  Ctrl+P 置顶  ·  Ctrl+D 删除历史  ·  Esc 隐藏",
            FontId::new(14.0, FontFamily::Proportional),
            Color32::from_rgb(139, 148, 158),
        );
    }

    fn subtitle_for(&self, entry: &Entry) -> String {
        match entry.kind {
            EntryKind::Current => "立即将 Desktop 切换到当前工作目录".to_owned(),
            EntryKind::PickOther => "浏览文件夹并切换，不迁移已有文件".to_owned(),
            EntryKind::Saved => {
                let Some(path) = entry.path.as_ref() else {
                    return entry.subtitle.clone();
                };

                if default_desktop_path()
                    .as_ref()
                    .map(|default| same_path(default, path))
                    .unwrap_or(false)
                {
                    return "恢复默认桌面路径".to_owned();
                }

                if entry.pinned {
                    return "已置顶 · 常用桌面目录".to_owned();
                }

                if self
                    .state
                    .history
                    .first()
                    .map(|saved| same_path(path, Path::new(saved)))
                    .unwrap_or(false)
                    || entry.subtitle == "最近切换的桌面目录"
                {
                    return "最近切换的桌面目录".to_owned();
                }

                if entry.subtitle == "当前系统 Desktop 路径" {
                    return "当前系统 Desktop 路径".to_owned();
                }

                entry.subtitle.clone()
            }
        }
    }
}

impl eframe::App for EasyDesktopApp {
    fn clear_color(&self, _visuals: &egui::Visuals) -> [f32; 4] {
        [0.0, 0.0, 0.0, 0.0]
    }

    fn logic(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.process_app_commands(ctx);
        if self.window_visible {
            ctx.request_repaint_after(Duration::from_millis(100));
        }
    }

    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        self.process_app_commands(&ctx);
        self.update_clock();

        if !self.window_visible {
            return;
        }

        self.handle_shortcuts(&ctx);

        let full = ui.max_rect();
        self.draw_background(ui, full);

        let palette_rect = Rect::from_center_size(
            full.center(),
            egui::vec2(WINDOW_WIDTH.min(full.width()), WINDOW_HEIGHT.min(full.height())),
        );
        self.draw_palette(ui, palette_rect);

        if ui.input(|i| i.pointer.primary_down()) {
            if let Some(pos) = ui.input(|i| i.pointer.interact_pos()) {
                let drag_rect =
                    Rect::from_min_size(palette_rect.min, egui::vec2(palette_rect.width(), 88.0));
                if drag_rect.contains(pos) {
                    ctx.send_viewport_cmd(egui::ViewportCommand::StartDrag);
                }
            }
        }

        let content = palette_rect.shrink2(egui::vec2(18.0, 16.0));
        let header_rect = Rect::from_min_size(content.min, egui::vec2(content.width(), HEADER_HEIGHT));
        let footer_rect = Rect::from_min_size(
            Pos2::new(content.left(), content.bottom() - FOOTER_HEIGHT),
            egui::vec2(content.width(), FOOTER_HEIGHT),
        );
        let status_rect = Rect::from_min_size(
            Pos2::new(content.left() + 6.0, footer_rect.top() - STATUS_HEIGHT - 8.0),
            egui::vec2(content.width() - 12.0, STATUS_HEIGHT),
        );
        let list_rect = Rect::from_min_max(
            Pos2::new(content.left(), header_rect.bottom() + 10.0),
            Pos2::new(content.right(), status_rect.top() - 8.0),
        );

        if self.draw_header(ui, header_rect) {
            ctx.send_viewport_cmd(egui::ViewportCommand::Close);
        }
        self.draw_list(ui, list_rect);

        ui.painter().text(
            Pos2::new(status_rect.left() + 4.0, status_rect.center().y),
            Align2::LEFT_CENTER,
            &self.status,
            FontId::new(13.0, FontFamily::Proportional),
            Color32::from_rgb(148, 163, 184),
        );
        ui.painter().text(
            Pos2::new(status_rect.right() - 4.0, status_rect.center().y),
            Align2::RIGHT_CENTER,
            SUMMON_HOTKEY_LABEL,
            FontId::new(13.0, FontFamily::Monospace),
            Color32::from_rgb(148, 163, 184),
        );

        self.draw_footer(ui, footer_rect);
    }
}

fn configure_fonts(ctx: &egui::Context) {
    let mut fonts = FontDefinitions::default();
    let mut proportional_chain: Vec<String> = Vec::new();
    let mut monospace_chain: Vec<String> = Vec::new();

    for (name, path) in [
        ("segoe_ui", r"C:\Windows\Fonts\segoeui.ttf"),
        ("microsoft_yahei", r"C:\Windows\Fonts\msyh.ttc"),
        ("noto_sans_sc", r"C:\Windows\Fonts\NotoSansSC-VF.ttf"),
        ("simhei", r"C:\Windows\Fonts\simhei.ttf"),
    ] {
        if let Ok(bytes) = fs::read(path) {
            fonts
                .font_data
                .insert(name.to_owned(), FontData::from_owned(bytes).into());
            proportional_chain.push(name.to_owned());
            monospace_chain.push(name.to_owned());
        }
    }

    let proportional = fonts
        .families
        .entry(FontFamily::Proportional)
        .or_default();
    for name in proportional_chain.iter().rev() {
        proportional.insert(0, name.clone());
    }

    let monospace = fonts.families.entry(FontFamily::Monospace).or_default();
    for name in monospace_chain.iter().rev() {
        monospace.insert(0, name.clone());
    }

    ctx.set_fonts(fonts);
}

fn configure_style(ctx: &egui::Context) {
    let mut style = (*ctx.global_style()).clone();
    style.spacing.item_spacing = egui::vec2(0.0, 0.0);
    style.spacing.button_padding = egui::vec2(0.0, 0.0);
    style.visuals = egui::Visuals::dark();
    style.visuals.panel_fill = Color32::TRANSPARENT;
    style.visuals.window_fill = Color32::TRANSPARENT;
    style.visuals.extreme_bg_color = Color32::from_rgba_premultiplied(18, 22, 28, 180);
    style.visuals.selection.bg_fill = Color32::from_rgba_premultiplied(34, 167, 255, 72);
    style.visuals.widgets.noninteractive.fg_stroke.color = Color32::from_rgb(244, 244, 245);
    style.visuals.widgets.inactive.fg_stroke.color = Color32::from_rgb(244, 244, 245);
    style.visuals.widgets.hovered.fg_stroke.color = Color32::from_rgb(244, 244, 245);
    style.visuals.widgets.active.fg_stroke.color = Color32::from_rgb(244, 244, 245);
    style.visuals.widgets.inactive.corner_radius = CornerRadius::same(12);
    style.visuals.widgets.hovered.corner_radius = CornerRadius::same(12);
    style.visuals.widgets.active.corner_radius = CornerRadius::same(12);
    style.text_styles.insert(
        egui::TextStyle::Body,
        FontId::new(16.0, FontFamily::Proportional),
    );
    style.text_styles.insert(
        egui::TextStyle::Button,
        FontId::new(14.0, FontFamily::Proportional),
    );
    style.text_styles.insert(
        egui::TextStyle::Heading,
        FontId::new(22.0, FontFamily::Proportional),
    );
    ctx.set_global_style(style);
}

#[cfg(windows)]
fn setup_global_hotkey(ctx: &egui::Context) -> (Receiver<AppCommand>, Option<HotKeyRegistration>) {
    let (tx, rx) = mpsc::channel();

    let Ok(manager) = GlobalHotKeyManager::new() else {
        return (rx, None);
    };

    let hotkey = HotKey::new(Some(Modifiers::CONTROL), Code::KeyE);
    let hotkey_id = hotkey.id();
    if manager.register(hotkey).is_err() {
        return (rx, None);
    }

    let ctx = ctx.clone();
    thread::spawn(move || loop {
        let Ok(event) = GlobalHotKeyEvent::receiver().recv() else {
            break;
        };

        if event.id == hotkey_id && event.state == HotKeyState::Pressed {
            let _ = tx.send(AppCommand::TogglePalette);
            ctx.request_repaint();
        }
    });

    (rx, Some(HotKeyRegistration { _manager: manager }))
}

#[cfg(not(windows))]
fn setup_global_hotkey(_ctx: &egui::Context) -> (Receiver<AppCommand>, Option<HotKeyRegistration>) {
    let (_tx, rx) = mpsc::channel();
    (rx, None)
}

fn draw_divider(painter: &egui::Painter, left: f32, right: f32, y: f32, color: Color32) {
    painter.line_segment([Pos2::new(left, y), Pos2::new(right, y)], Stroke::new(1.0, color));
}

fn draw_search_icon(
    painter: &egui::Painter,
    center: Pos2,
    radius: f32,
    color: Color32,
    stroke_width: f32,
) {
    painter.circle_stroke(center, radius, Stroke::new(stroke_width, color));
    painter.line_segment(
        [
            center + egui::vec2(radius * 0.58, radius * 0.58),
            center + egui::vec2(radius * 1.24, radius * 1.24),
        ],
        Stroke::new(stroke_width, color),
    );
}

fn draw_close_icon(
    painter: &egui::Painter,
    center: Pos2,
    radius: f32,
    color: Color32,
    stroke_width: f32,
) {
    painter.line_segment(
        [
            center + egui::vec2(-radius, -radius),
            center + egui::vec2(radius, radius),
        ],
        Stroke::new(stroke_width, color),
    );
    painter.line_segment(
        [
            center + egui::vec2(radius, -radius),
            center + egui::vec2(-radius, radius),
        ],
        Stroke::new(stroke_width, color),
    );
}

fn draw_star_icon(painter: &egui::Painter, center: Pos2, radius: f32, color: Color32) {
    let mut points = Vec::with_capacity(10);
    let inner_radius = radius * 0.46;
    for idx in 0..10 {
        let angle = -std::f32::consts::FRAC_PI_2 + idx as f32 * std::f32::consts::PI / 5.0;
        let r = if idx % 2 == 0 { radius } else { inner_radius };
        points.push(center + egui::vec2(angle.cos() * r, angle.sin() * r));
    }
    painter.add(egui::Shape::convex_polygon(points, color, Stroke::NONE));
}

fn draw_folder_search_icon(
    painter: &egui::Painter,
    rect: Rect,
    folder_color: Color32,
    stroke_color: Color32,
) {
    let tab = Rect::from_min_size(
        rect.min + egui::vec2(2.0, 4.0),
        egui::vec2(rect.width() * 0.46, rect.height() * 0.24),
    );
    let body = Rect::from_min_size(
        rect.min + egui::vec2(1.0, 10.0),
        egui::vec2(rect.width() * 0.88, rect.height() * 0.60),
    );

    painter.rect_filled(tab, CornerRadius::same(6), folder_color);
    painter.rect_filled(body, CornerRadius::same(8), folder_color);

    let lens_center = Pos2::new(rect.right() - 8.0, rect.bottom() - 7.0);
    painter.circle_stroke(lens_center, 8.5, Stroke::new(3.0, stroke_color));
    painter.circle_stroke(
        lens_center,
        7.0,
        Stroke::new(2.0, Color32::from_rgba_premultiplied(90, 232, 255, 240)),
    );
    painter.line_segment(
        [
            lens_center + egui::vec2(5.6, 5.6),
            lens_center + egui::vec2(12.0, 12.0),
        ],
        Stroke::new(3.0, stroke_color),
    );
    painter.line_segment(
        [
            lens_center + egui::vec2(4.8, 4.8),
            lens_center + egui::vec2(11.0, 11.0),
        ],
        Stroke::new(2.0, Color32::from_rgba_premultiplied(90, 232, 255, 240)),
    );
}

fn app_state_path() -> PathBuf {
    let mut base = dirs::data_local_dir().unwrap_or_else(|| PathBuf::from("."));
    base.push("EasyDesktop");
    base.push("ui-state.json");
    base
}

fn load_state(path: &Path) -> SavedState {
    let Ok(content) = fs::read_to_string(path) else {
        return SavedState::default();
    };
    serde_json::from_str(&content).unwrap_or_default()
}

fn save_state(path: &Path, state: &SavedState) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("无法创建配置目录: {e}"))?;
    }
    let content = serde_json::to_string_pretty(state).map_err(|e| e.to_string())?;
    fs::write(path, content).map_err(|e| e.to_string())
}

fn dedup_paths(items: &[String]) -> Vec<PathBuf> {
    let mut seen = HashSet::new();
    let mut out = Vec::new();

    for item in items {
        let path = PathBuf::from(item);
        let key = path.to_string_lossy().to_lowercase();
        if seen.insert(key) {
            out.push(path);
        }
    }

    out
}

fn history_rank(history: &[String], path: &Path) -> usize {
    history
        .iter()
        .position(|saved| same_path(path, Path::new(saved)))
        .unwrap_or(usize::MAX)
}

fn same_path(left: &Path, right: &Path) -> bool {
    left.to_string_lossy()
        .eq_ignore_ascii_case(&right.to_string_lossy())
}

fn current_clock_text() -> String {
    Local::now().format("%H:%M").to_string()
}

fn default_desktop_path() -> Option<PathBuf> {
    dirs::home_dir().map(|path| path.join("Desktop"))
}

#[cfg(windows)]
fn read_desktop_path() -> Result<Option<PathBuf>, String> {
    let hkcu = RegKey::predef(HKEY_CURRENT_USER);
    let key = hkcu
        .open_subkey("Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\User Shell Folders")
        .map_err(|e| e.to_string())?;
    let raw: String = key.get_value("Desktop").map_err(|e| e.to_string())?;
    let expanded = expand_windows_env_vars(&raw);
    Ok(Some(PathBuf::from(expanded)))
}

#[cfg(not(windows))]
fn read_desktop_path() -> Result<Option<PathBuf>, String> {
    Ok(default_desktop_path())
}

#[cfg(windows)]
fn set_desktop_path(path: &Path) -> Result<(), String> {
    if !path.exists() {
        fs::create_dir_all(path).map_err(|e| format!("创建目录失败: {e}"))?;
    }

    let wide_path = wide_null(path.as_os_str());
    let hr = unsafe { SHSetKnownFolderPath(&FOLDERID_DESKTOP, 0, 0, wide_path.as_ptr()) };
    if hr < 0 {
        return Err(format!("SHSetKnownFolderPath 失败: 0x{hr:08X}"));
    }

    notify_shell_folder_change(path);

    Ok(())
}

#[cfg(not(windows))]
fn set_desktop_path(_path: &Path) -> Result<(), String> {
    Err("当前系统不支持写入 Windows 桌面路径".to_owned())
}

#[cfg(windows)]
fn expand_windows_env_vars(raw: &str) -> String {
    let chars: Vec<char> = raw.chars().collect();
    let mut out = String::new();
    let mut idx = 0usize;

    while idx < chars.len() {
        if chars[idx] == '%' {
            let mut end = idx + 1;
            while end < chars.len() && chars[end] != '%' {
                end += 1;
            }

            if end < chars.len() && end > idx + 1 {
                let name: String = chars[idx + 1..end].iter().collect();
                if let Ok(value) = std::env::var(&name) {
                    out.push_str(&value);
                } else {
                    out.push('%');
                    out.push_str(&name);
                    out.push('%');
                }
                idx = end + 1;
                continue;
            }
        }

        out.push(chars[idx]);
        idx += 1;
    }

    out
}

#[cfg(windows)]
fn notify_shell_folder_change(path: &Path) {
    let wide_path = wide_null(path.as_os_str());
    unsafe {
        SHChangeNotify(
            SHCNE_ASSOCCHANGED,
            SHCNF_IDLIST | SHCNF_FLUSH,
            std::ptr::null(),
            std::ptr::null(),
        );
        SHChangeNotify(
            SHCNE_UPDATEDIR,
            SHCNF_PATHW | SHCNF_FLUSH,
            wide_path.as_ptr() as *const _,
            std::ptr::null(),
        );
    }

    broadcast_setting_change("User Shell Folders");
    broadcast_setting_change("Environment");
}

#[cfg(windows)]
fn broadcast_setting_change(area: &str) {
    let wide_area = wide_null(std::ffi::OsStr::new(area));
    let mut result = 0usize;
    unsafe {
        let _ = SendMessageTimeoutW(
            HWND_BROADCAST,
            WM_SETTINGCHANGE,
            0,
            wide_area.as_ptr() as isize,
            SMTO_ABORTIFHUNG,
            5_000,
            &mut result,
        );
    }
}

#[cfg(windows)]
fn wide_null(value: &std::ffi::OsStr) -> Vec<u16> {
    value.encode_wide().chain(std::iter::once(0)).collect()
}
