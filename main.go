// ei-projects: a project cockpit TUI over the same watched folders the
// ei.git-status bar widget owns -- repositories with their git state,
// opencode sessions, and the newest notes the vault holds for each project.
//
//	ei-projects          open the cockpit
//	ei-projects scan     print the report JSON (the contract for anything else)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/scan"
	"github.com/santiagoei/ei-projects/internal/tui"
)

// version is stamped by the build; keep in sync with git tags.
var version = "0.1.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "scan" {
		scanCmd(os.Args[2:])
		return
	}

	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
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

func scanCmd(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	pretty := fs.Bool("pretty", false, "indent the JSON")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ei-projects:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := scan.Run(ctx, cfg).JSON(*pretty)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ei-projects:", err)
		os.Exit(1)
	}
	fmt.Println(out)
}
