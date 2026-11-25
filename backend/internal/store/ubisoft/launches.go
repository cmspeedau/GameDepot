// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// launches.go derives launch documents from the ownership service's per-game install
// configuration (YAML): root.name is the title and the start_game.online.executables[]
// entries (falling back to offline) are the launchers - Ubisoft's counterpart to GOG's
// playTasks. Only install-relative executables count; register/absolute paths are
// skipped.
//
// Two config generations exist live (both verified Nov 2026): modern titles label
// entries with `- shortcut_name:` (Anno 2070), older ones start entries with `- path:`
// and only carry `description:` labels plus optional `arguments:` (AC3, Rayman).
// Entries may sit at the same indent as their `executables:` key.
package ubisoft

import "strings"

// UbisoftLaunchDocument is one launchable the release is seeded with.
type UbisoftLaunchDocument struct {
	Name       string
	Executable string
	Arguments  []string
}

// DeriveUbisoftLaunches parses an install configuration (YAML-ish) and returns the
// title and the launch executables. It mirrors the vendored reference scanner.
func DeriveUbisoftLaunches(configYaml string) (title string, launches []UbisoftLaunchDocument) {
	launches = []UbisoftLaunchDocument{}
	lines := strings.Split(configYaml, "\n")
	// normalise CRLF already handled by split on \n; trim trailing \r
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	titleIndent := -1
	startGameIndent := -1
	blockIndent := -1 // online:/offline: anchor; -2 = skipping this block
	blockDone := false
	executablesIndent := -1
	var current *struct {
		name  string
		label string
		exe   string
		args  []string
	}
	inPath := false
	seen := map[string]bool{}

	finishEntry := func() {
		if current != nil && current.exe != "" {
			key := current.name + "\x00" + current.exe + "\x00" + strings.Join(current.args, " ")
			if !seen[key] {
				seen[key] = true
				name := current.name
				if name == "" {
					name = current.label
				}
				if name == "" {
					name = "Play"
				}
				launches = append(launches, UbisoftLaunchDocument{
					Name:       name,
					Executable: current.exe,
					Arguments:  current.args,
				})
				if blockIndent >= 0 {
					blockDone = true
				}
			}
		}
		current = nil
		inPath = false
	}

	deeper := func(indent, anchor int) bool {
		return anchor >= 0 && indent > anchor
	}

	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " \t"))
		text := trimmed

		if startGameIndent < 0 {
			if text == "root:" {
				titleIndent = indent
			} else if title == "" && deeper(indent, titleIndent) && strings.HasPrefix(text, "name:") {
				title = stripQuotes(strings.TrimSpace(text[len("name:"):]))
			}
			if text == "start_game:" {
				startGameIndent = indent
			}
			continue
		}

		if indent <= startGameIndent {
			finishEntry()
			startGameIndent = -1
			blockIndent = -1
			blockDone = false
			executablesIndent = -1
			continue
		}
		if text == "online:" || text == "offline:" {
			finishEntry()
			if blockDone || (text == "offline:" && len(launches) > 0) {
				blockIndent = -2
			} else {
				blockIndent = indent
			}
			executablesIndent = -1
			continue
		}
		if blockIndent == -2 {
			continue
		}
		if text == "executables:" {
			finishEntry()
			executablesIndent = indent
			continue
		}
		isItem := strings.HasPrefix(text, "- ")
		if executablesIndent >= 0 && indent <= executablesIndent && !isItem {
			finishEntry()
			executablesIndent = -1
		}
		if executablesIndent < 0 {
			continue
		}

		item := text
		if isItem {
			item = strings.TrimSpace(text[2:])
			finishEntry()
			current = &struct {
				name  string
				label string
				exe   string
				args  []string
			}{args: []string{}}
			if item == "" {
				continue
			}
		}
		if current == nil {
			current = &struct {
				name  string
				label string
				exe   string
				args  []string
			}{args: []string{}}
		}

		switch {
		case strings.HasPrefix(item, "shortcut_name:"):
			current.name = stripQuotes(strings.TrimSpace(item[len("shortcut_name:"):]))
		case strings.HasPrefix(item, "description:"):
			if current.label == "" {
				current.label = stripQuotes(strings.TrimSpace(item[len("description:"):]))
			}
		case item == "path:":
			inPath = true
		case strings.HasPrefix(item, "working_directory:"):
			inPath = false
		case inPath && strings.HasPrefix(item, "relative:"):
			exe := stripQuotes(strings.TrimSpace(item[len("relative:"):]))
			exe = strings.ReplaceAll(exe, "\\", "/")
			exe = strings.TrimLeft(exe, "/")
			current.exe = exe
			inPath = false
		case inPath && (strings.HasPrefix(item, "register:") || strings.HasPrefix(item, "absolute:")):
			inPath = false
		case strings.HasPrefix(item, "arguments:"):
			raw := stripQuotes(strings.TrimSpace(item[len("arguments:"):]))
			if raw != "" {
				current.args = append(current.args, strings.Fields(raw)...)
			}
		}
	}
	finishEntry()
	return title, launches
}

func stripQuotes(value string) string {
	v := strings.TrimSpace(value)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}
