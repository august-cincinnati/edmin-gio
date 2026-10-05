package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gioui.org/io/key"
	"gioui.org/layout"
)

// Theme holds every colour EdMin draws with.
type Theme struct {
	Name string

	BG, FG    string // editor and list surfaces
	Panel     string // window chrome: header bar, tabs, status bar
	Border    string
	Hover     string
	Selection string
	Dim       string // line numbers in search results
	MatchBG   string // search matches
	MatchFG   string
	JumpLine  string // line highlighted after a jump
	TermBG    string
	TermFG    string
	Syntax    map[string]string // highlight group → foreground
	DefMarker string            // "def" label in symbol results
}

var themes = []*Theme{
	{
		Name: "Light", BG: "#ffffff", FG: "#1f1f1f", Panel: "#f3f3f3", Border: "#d4d4d4", Hover: "#e4e4e4",
		Selection: "#add6ff", Dim: "#888888", MatchBG: "#ffe066", MatchFG: "#1f1f1f", JumpLine: "#e8f0fe",
		TermBG: "#1e1e1e", TermFG: "#d4d4d4", DefMarker: "#267f99",
		Syntax: map[string]string{
			hlKeyword: "#af00db", hlString: "#a31515", hlComment: "#008000", hlNumber: "#098658",
			hlType: "#267f99", hlFunction: "#795e26", hlConstant: "#0000ff", hlProperty: "#001080",
		},
	},
	{
		Name: "Dark", BG: "#1e1e1e", FG: "#d4d4d4", Panel: "#252526", Border: "#3c3c3c", Hover: "#2f3033",
		Selection: "#264f78", Dim: "#858585", MatchBG: "#9e6a03", MatchFG: "#ffffff", JumpLine: "#2a3550",
		TermBG: "#181818", TermFG: "#d4d4d4", DefMarker: "#4ec9b0",
		Syntax: map[string]string{
			hlKeyword: "#c586c0", hlString: "#ce9178", hlComment: "#6a9955", hlNumber: "#b5cea8",
			hlType: "#4ec9b0", hlFunction: "#dcdcaa", hlConstant: "#569cd6", hlProperty: "#9cdcfe",
		},
	},
	{
		Name: "Tan", BG: "#f4ecd8", FG: "#433422", Panel: "#e9dfc7", Border: "#cdbf9e", Hover: "#dfd3b6",
		Selection: "#d9c7a0", Dim: "#8c7b61", MatchBG: "#f2c14e", MatchFG: "#2e2416", JumpLine: "#ebdcb8",
		TermBG: "#3c3428", TermFG: "#ebdbb2", DefMarker: "#076678",
		Syntax: map[string]string{
			hlKeyword: "#9d0006", hlString: "#79740e", hlComment: "#928374", hlNumber: "#8f3f71",
			hlType: "#b57614", hlFunction: "#076678", hlConstant: "#af3a03", hlProperty: "#427b58",
		},
	},
	{
		Name: "Solarized Dark", BG: "#002b36", FG: "#93a1a1", Panel: "#073642", Border: "#0e4b59", Hover: "#0a4250",
		Selection: "#11505f", Dim: "#657b83", MatchBG: "#b58900", MatchFG: "#002b36", JumpLine: "#0b3d49",
		TermBG: "#002b36", TermFG: "#93a1a1", DefMarker: "#2aa198",
		Syntax: map[string]string{
			hlKeyword: "#859900", hlString: "#2aa198", hlComment: "#586e75", hlNumber: "#d33682",
			hlType: "#b58900", hlFunction: "#268bd2", hlConstant: "#cb4b16", hlProperty: "#6c71c4",
		},
	},
	{
		Name: "Rust", BG: "#2b1a17", FG: "#e8d5c8", Panel: "#3a221d", Border: "#5a342b", Hover: "#462a24",
		Selection: "#6e3a2c", Dim: "#9c7d70", MatchBG: "#d9822b", MatchFG: "#2b1a17", JumpLine: "#3f2620",
		TermBG: "#241512", TermFG: "#e8d5c8", DefMarker: "#e8a87c",
		Syntax: map[string]string{
			hlKeyword: "#e0613a", hlString: "#c5b26a", hlComment: "#8a6c60", hlNumber: "#e8a87c",
			hlType: "#f0b45a", hlFunction: "#f2d0a4", hlConstant: "#d4766a", hlProperty: "#c99a8a",
		},
	},
	{
		Name: "Green", BG: "#16241c", FG: "#d4e4d6", Panel: "#1c2e24", Border: "#2e4a3a", Hover: "#24392d",
		Selection: "#2f5a42", Dim: "#7a9483", MatchBG: "#b8a43a", MatchFG: "#16241c", JumpLine: "#203529",
		TermBG: "#121e17", TermFG: "#d4e4d6", DefMarker: "#7fd1a8",
		Syntax: map[string]string{
			hlKeyword: "#8fd16a", hlString: "#d6c27a", hlComment: "#5f7d6a", hlNumber: "#e09f6b",
			hlType: "#7fd1a8", hlFunction: "#b5e8a0", hlConstant: "#6bbfd1", hlProperty: "#a8c9b4",
		},
	},
	{
		Name: "Purple", BG: "#1f1a2e", FG: "#ddd6f0", Panel: "#271f3a", Border: "#3e3359", Hover: "#2f2645",
		Selection: "#4a3a72", Dim: "#8a80a6", MatchBG: "#c792ea", MatchFG: "#1f1a2e", JumpLine: "#2c2442",
		TermBG: "#1a1626", TermFG: "#ddd6f0", DefMarker: "#89ddff",
		Syntax: map[string]string{
			hlKeyword: "#c792ea", hlString: "#c3e88d", hlComment: "#6e6690", hlNumber: "#f78c6c",
			hlType: "#ffcb6b", hlFunction: "#82aaff", hlConstant: "#ff5370", hlProperty: "#b4a7e8",
		},
	},
}

func themeByName(name string) *Theme {
	for _, t := range themes {
		if t.Name == name {
			return t
		}
	}
	return themes[0]
}

// ---- Persisted settings ----

type Settings struct {
	Theme string `json:"theme,omitempty"`
	// Shell is the command a project's terminals run, as a list of
	// arguments. Only used in the project file; empty means detect it.
	Shell []string `json:"shell,omitempty"`
	// Open lists the project folders open in windows, reopened when EdMin
	// starts without arguments. Only used in the user-wide file.
	Open []string `json:"open,omitempty"`
}

// settingsPath is the user-wide settings file. Its theme is the default for
// projects that haven't chosen their own.
func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "edmin", "settings.json")
}

// projectSettingsPath holds the theme chosen for one project.
func projectSettingsPath(root string) string {
	return filepath.Join(root, ".edmin", "settings.json")
}

func loadSettings(p string) Settings {
	var s Settings
	if p != "" {
		if data, err := os.ReadFile(p); err == nil {
			json.Unmarshal(data, &s)
		}
	}
	return s
}

// projectTheme is the theme saved for root, or else the user's default.
func projectTheme(root string) *Theme {
	if s := loadSettings(projectSettingsPath(root)); s.Theme != "" {
		return themeByName(s.Theme)
	}
	return themeByName(loadSettings(settingsPath()).Theme)
}

func saveSettings(p string, s Settings) error {
	if p == "" {
		return fmt.Errorf("no user config directory")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

// ---- Settings window ----

// setTheme switches this window to t and restyles everything open in it.
func (a *App) setTheme(t *Theme) {
	a.theme = t
	a.search.restyle()
	a.invalidate()
}

func (a *App) showSettings() {
	d := &Dialog{Title: "Settings", White: true, Width: 320, Default: RespNone}
	a.settingsDlg = d
	radios := make([]Toggle, len(themes))
	d.Body = func(gtx layout.Context) layout.Dimensions {
		pal := d.pal()
		for i, t := range themes {
			radios[i].On = t == a.theme
			if radios[i].Changed(gtx, true) && t != a.theme {
				a.chooseTheme(t)
			}
		}
		return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			items := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layoutSpans(gtx, []TextSpan{
					{Text: "Theme ", Color: pal.FG, Weight: 700},
					{Text: "(this window)", Color: pal.FG, Size: small(uiSize)},
				})
			}), layout.Rigid(spacer(0, 6))}
			for i, t := range themes {
				items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return radios[i].Layout(gtx, pal, t.Name, true)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
		})
	}
	d.addButtons("Close", RespClose)
	d.OnKey = func(gtx layout.Context, e key.Event) bool {
		n := panelDigit(e.Name)
		mods := shortcutMods(e.Modifiers)
		if n == 0 || mods != key.ModCtrl && mods != key.ModCtrl|key.ModShift {
			return false
		}
		a.panelKey(n, mods&key.ModShift != 0)
		return true
	}
	d.OnResponse = func(int) { a.settingsDlg = nil }
	a.showDialog(d)
}

// chooseTheme applies t and remembers it for this project, and as the
// default for new ones.
func (a *App) chooseTheme(t *Theme) {
	a.setTheme(t)
	ps := loadSettings(projectSettingsPath(a.root))
	ps.Theme = t.Name
	err := saveSettings(projectSettingsPath(a.root), ps)
	if err == nil {
		s := loadSettings(settingsPath())
		s.Theme = t.Name
		err = saveSettings(settingsPath(), s)
	}
	if err != nil {
		a.setStatusMsg("Could not save settings: " + err.Error())
	}
}
