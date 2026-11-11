// ei-projects: a project cockpit TUI over the same watched folders the
// ei.git-status bar widget owns -- repositories with their git state,
// opencode sessions, and the newest notes the vault holds for each project.
//
//	ei-projects                    open the cockpit
//	ei-projects scan               print the report JSON (the contract for anything else)
//	ei-projects code [proj]        open a project's repository in the editor
//	ei-projects agent [proj]       start the default agent in one
//	ei-projects terminal [proj]    open a terminal in one
//	ei-projects resume [id]        reopen the newest session, or a given one
//
// The last four are the cockpit's actions without the cockpit: the same
// launchers a keybind can call directly. `[proj]` is a project or repository
// name (or its number in the list); without one, the repository the cockpit
// last had selected. --help lists the flags they take.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/scan"
	"github.com/santiagoei/ei-projects/internal/tui"
)

// version is stamped by the build; keep in sync with git tags.
var version = "0.1.0"

func main() {
	switch arg := arg(); {
	case startsCockpit(arg):
		cockpit()
	case arg == "help" || arg == "-h" || arg == "--help":
		usage()
	case isCommand(arg):
		run(arg, rest())
	default:
		fmt.Fprintf(os.Stderr, "ei-projects: unknown command %q\n", arg)
		usage()
		os.Exit(2)
	}
}

// commands is every verb that runs without the cockpit, and the one place
// that says so -- an unknown word reaches the usage text, a flag does not.
var commands = []string{"scan", "code", "agent", "terminal", "resume"}

func isCommand(arg string) bool {
	for _, c := range commands {
		if c == arg {
			return true
		}
	}
	return false
}

// startsCockpit: no argument at all, or a flag for the cockpit itself
// (--version). A flag never reaches the command switch, or the version string
// becomes an "unknown command" -- which is exactly the regression this guards.
func startsCockpit(arg string) bool {
	return arg == "" || strings.HasPrefix(arg, "-")
}

func arg() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return ""
}

func rest() []string {
	if len(os.Args) > 2 {
		return os.Args[2:]
	}
	return nil
}

// cockpit is the interactive program: the flags it takes are parsed here, and
// only here -- a command word in argv[1] never reaches flag.Parse.
func cockpit() {
	fs := flag.NewFlagSet("ei-projects", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("ei-projects " + version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ei-projects:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(tui.New(cfg), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ei-projects:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, usageText())
}

func usageText() string {
	return `usage: ei-projects [command] [flags] [project]

  (no command)          open the cockpit
  scan                  print the report JSON (--pretty)
  code                  open a project's repository in the editor (--last, --all)
  agent                 start the default agent in a repository (--last)
  terminal              open a terminal in a repository (--last)
  resume                reopen the newest opencode session (--project, --last)
  help                  this text

[project] is a project or repository name, or its number in the watched list.
Without one, the repository the cockpit last had selected.
`
}

// resolveFlags is the flag set the launch commands share: which repository,
// and what to do when the name is not given.
type resolveFlags struct {
	Project string
	Last    bool
	All     bool
	Pretty  bool
}

// run executes one of the non-interactive verbs and exits on failure.
func run(name string, args []string) {
	switch name {
	case "scan":
		scanCmd(parseFlags("scan", args))
		return
	case "code", "agent", "terminal":
		f := parseFlags(name, args)
		path, err := resolve(f)
		if err != nil {
			fail(err)
		}
		switch name {
		case "code":
			err = actions.Editor(path) // --all means the folder, not six windows
		case "agent":
			err = actions.Agent(path)
		case "terminal":
			err = actions.Terminal(path)
		}
		if err != nil {
			fail(fmt.Errorf("%s %s: %w", name, path, err))
		}
		fmt.Println(path)
		return
	case "resume":
		if err := resumeCmd(parseFlags(name, args)); err != nil {
			fail(err)
		}
	}
}

func parseFlags(name string, args []string) resolveFlags {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var f resolveFlags
	fs.StringVar(&f.Project, "project", "", "project or repository to act on")
	fs.StringVar(&f.Project, "p", "", "shorthand for -project")
	fs.BoolVar(&f.Last, "last", false, "the repository the cockpit last had selected")
	fs.BoolVar(&f.All, "all", false, "every repository of the project (code)")
	fs.BoolVar(&f.Pretty, "pretty", false, "indent the JSON (scan)")
	fs.Parse(args)
	if len(fs.Args()) > 0 && f.Project == "" {
		f.Project = strings.Join(fs.Args(), " ")
	}
	return f
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ei-projects:", err)
	os.Exit(1)
}

// resolve turns what the user typed into one directory. The names are the
// cockpit's own: a project's name or number, or any repository inside it.
// Nothing given means the seat the cockpit left off at.
func resolve(f resolveFlags) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report := scan.Run(ctx, cfg)

	if f.Project == "" {
		if f.All {
			return "", fmt.Errorf("-all needs a project: ei-projects code --all [project]")
		}
		if path, ok := tui.LastSelection(); ok {
			return path, nil
		}
		if len(report.Projects) == 0 {
			return "", fmt.Errorf("no projects to act on")
		}
		return report.Projects[0].Path, nil
	}

	var names []string
	for i, p := range report.Projects {
		names = append(names, fmt.Sprintf("%d=%s", i+1, p.Name))
	}
	want := strings.ToLower(strings.TrimSpace(f.Project))
	for i, p := range report.Projects {
		if strings.EqualFold(p.Name, want) || want == fmt.Sprintf("%d", i+1) {
			if p.Missing {
				return "", fmt.Errorf("project %s is not on disk", p.Name)
			}
			if f.All {
				return p.Path, nil
			}
			// a project is a folder of repositories: the cockpit would open
			// the one it has selected, so act on the same thing -- the first
			// repository of the project, or the project itself if it is one
			if len(p.Repos) > 0 {
				if path, ok := tui.LastSelection(); ok {
					for _, r := range p.Repos {
						if r.Path == path {
							return path, nil
						}
					}
				}
				return p.Repos[0].Path, nil
			}
			return p.Path, nil
		}
		for _, r := range p.Repos {
			if strings.EqualFold(base(r.Path), want) {
				return r.Path, nil
			}
		}
	}
	return "", fmt.Errorf("no project or repository %q (%s)", f.Project, strings.Join(names, ", "))
}

func base(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// resumeCmd reopens the newest session, or the one named: the same jump the
// cockpit's `s` key performs, without opening the cockpit.
func resumeCmd(f resolveFlags) error {
	var (
		sessions []agent.Session
		err      error
	)
	id := f.Project // a positional here is a session id or a path
	if f.Last || id != "" && looksLikePath(id) {
		// --last and --project both mean "the newest session of that place"
		root := ""
		if path, perr := resolve(f); perr == nil {
			root = path
		} else {
			return perr
		}
		sessions, err = agent.Recent(root, 1)
		if err != nil {
			return err
		}
		id = ""
	} else if id != "" && !looksLikePath(id) {
		// an explicit session id: the directory is read from the database
		dir, found, derr := agent.SessionDir(id)
		if derr != nil {
			return derr
		}
		if !found {
			return fmt.Errorf("no session %q in opencode's database", id)
		}
		return actions.ResumeSession(dir, id)
	} else {
		cfg, cerr := config.Load()
		if cerr != nil {
			return cerr
		}
		sessions, err = agent.Recents(cfg.WatchDirs, 1)
		if err != nil {
			return err
		}
	}
	if len(sessions) == 0 {
		return fmt.Errorf("no opencode session to resume")
	}
	s := sessions[0]
	fmt.Printf("%s · %s\n", s.Title, s.Directory)
	return actions.ResumeSession(s.Directory, s.ID)
}

// looksLikePath tells "/home/me/proj" from "ses_01Ab..." -- the positional
// argument of resume can be either.
func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.Contains(s, "/")
}

// scanCmd is the JSON contract, for the panel and for scripts.
func scanCmd(f resolveFlags) {
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := scan.Run(ctx, cfg).JSON(f.Pretty)
	if err != nil {
		fail(err)
	}
	fmt.Println(out)
}
