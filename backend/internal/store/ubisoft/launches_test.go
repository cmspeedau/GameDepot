package ubisoft

import (
	"strings"
	"testing"
)

const annoConfig = `version: 2.0
root:
  name: Anno 2070
  background_image: dab54ae00427d76aa3cf655c4f8c4dde.jpg
  icon_image: fb2ebde69f7640f2ac8d00752b8a60bb.ico
  start_game:
    online:
      overlay_required: yes
      overlay_supported: yes
      executables:
      - shortcut_name: Anno2070
        path:
          relative: Anno5.exe
        working_directory:
          register: HKEY_LOCAL_MACHINE\\SOFTWARE\\Ubisoft\\Launcher\\Installs\\22\\InstallDir
        denuvo: yes
      pc_bang: {}
    offline:
      executables:
      - shortcut_name: Anno2070
        path:
          relative: Anno5.exe
        working_directory:
          register: HKEY_LOCAL_MACHINE\\SOFTWARE\\Ubisoft\\Launcher\\Installs\\22\\InstallDir
  rpe_available: yes
`

func TestDeriveUbisoftLaunchesAnno(t *testing.T) {
	title, launches := DeriveUbisoftLaunches(annoConfig)
	if title != "Anno 2070" {
		t.Fatalf("title = %q", title)
	}
	if len(launches) != 1 || launches[0].Name != "Anno2070" || launches[0].Executable != "Anno5.exe" {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestDeriveUbisoftLaunchesOfflineFallback(t *testing.T) {
	yaml := `root:
  name: Offline Game
  start_game:
    offline:
      executables:
      - shortcut_name: PlayOffline
        path:
          relative: game_offline.exe
`
	title, launches := DeriveUbisoftLaunches(yaml)
	if title != "Offline Game" {
		t.Fatalf("title = %q", title)
	}
	if len(launches) != 1 || launches[0].Name != "PlayOffline" || launches[0].Executable != "game_offline.exe" {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestDeriveUbisoftLaunchesSkipRegisterAndDedupe(t *testing.T) {
	yaml := `root:
  name: Registry Game
  start_game:
    online:
      executables:
      - shortcut_name: Registered
        path:
          register: HKEY_LOCAL_MACHINE\\SOFTWARE\\Whatever
      - shortcut_name: Dup
        path:
          relative: a.exe
      - shortcut_name: Dup
        path:
          relative: a.exe
      - shortcut_name: Second
        path:
          relative: sub\b.exe
`
	_, launches := DeriveUbisoftLaunches(yaml)
	if len(launches) != 2 || launches[0].Executable != "a.exe" || launches[1].Executable != "sub/b.exe" {
		t.Fatalf("launches = %+v", launches)
	}
}

const legacyConfig = `root:
  name: "Rayman Origins"
  sort_string: "Rayman 5"
  start_game:
    online:
      after_game_report_enabled: no
      executables:
        - path: 
            relative: Rayman Origins.exe
          working_directory:
            register: HKEY_LOCAL_MACHINE\\SOFTWARE\\UBISOFT\\Rayman Origins\\GameUpdate\\InstallDir
    offline:
      executables:
        - path:
            relative: Rayman Origins.exe
          arguments: -offline
`

const ac3Config = `root:
  name: l1
  sort_string: Assassin's Creed 05.1
  start_game:
    online:
      legacy_ticket_enabled: yes
      executables:
        - path:
            relative: AC3SP.exe
          working_directory:
            register: HKEY_LOCAL_MACHINE\\SOFTWARE\\UBISOFT\\Assassin's Creed 3\\GameUpdate\\installdir
          internal_name: Executable
          description: Single Player
        - path:
            relative: AC3MP.exe
          internal_name: Executable
          description: Multi Player
    offline:
      executables:
        - path:
            relative: AC3SP.exe
          arguments: -offline
          description: Single Player
`

func TestDeriveUbisoftLaunchesLegacy(t *testing.T) {
	title, launches := DeriveUbisoftLaunches(legacyConfig)
	if title != "Rayman Origins" {
		t.Fatalf("title = %q", title)
	}
	if len(launches) != 1 || launches[0].Name != "Play" || launches[0].Executable != "Rayman Origins.exe" {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestDeriveUbisoftLaunchesAC3(t *testing.T) {
	_, launches := DeriveUbisoftLaunches(ac3Config)
	if len(launches) != 2 || launches[0].Name != "Single Player" || launches[0].Executable != "AC3SP.exe" || launches[1].Name != "Multi Player" {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestDeriveUbisoftLaunchesOfflineArgs(t *testing.T) {
	offlineOnly := strings.Replace(ac3Config, "    online:\n      legacy_ticket_enabled: yes\n      executables:\n        - path:\n            relative: AC3SP.exe\n          working_directory:\n            register: HKEY_LOCAL_MACHINE\\\\SOFTWARE\\\\UBISOFT\\\\Assassin's Creed 3\\\\GameUpdate\\\\installdir\n          internal_name: Executable\n          description: Single Player\n        - path:\n            relative: AC3MP.exe\n          internal_name: Executable\n          description: Multi Player\n", "    offline:\n", -1)
	_, launches := DeriveUbisoftLaunches(offlineOnly)
	if len(launches) != 1 || launches[0].Executable != "AC3SP.exe" || len(launches[0].Arguments) != 1 || launches[0].Arguments[0] != "-offline" {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestDeriveUbisoftLaunchesNoStartGame(t *testing.T) {
	title, launches := DeriveUbisoftLaunches("root:\n  name: Bare\n  icon_image: x.ico\n")
	if title != "Bare" {
		t.Fatalf("title = %q", title)
	}
	if len(launches) != 0 {
		t.Fatalf("launches = %+v", launches)
	}
}
