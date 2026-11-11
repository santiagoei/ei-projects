package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectPrefersCandidatesOrder(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	repo := filepath.Join(root, "proj", "app")
	for _, d := range []string{proj, repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// nothing yet
	if d, f := Detect([]string{proj, repo}); d != "" || f != "" {
		t.Fatalf("clean dirs detected an env: %q %q", d, f)
	}

	// repo-level compose found under the repo candidate
	write := func(p string) {
		if err := os.WriteFile(p, []byte("services: {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "compose.yaml"))
	d, f := Detect([]string{proj, repo})
	if d != repo || f != filepath.Join(repo, "compose.yaml") {
		t.Fatalf("repo env: %q %q", d, f)
	}

	// a project-root file wins over the repo one (candidates are ordered)
	write(filepath.Join(proj, "docker-compose.yml"))
	d, f = Detect([]string{proj, repo})
	if d != proj || filepath.Base(f) != "docker-compose.yml" {
		t.Fatalf("project root must win: %q %q", d, f)
	}
}

func TestParsePsBothShapes(t *testing.T) {
	// modern: one pretty JSON array
	arr := []byte(`[
 {"ID":"a","Project":"p","Service":"moodle","State":"running","Health":"","Name":"p-moodle-1"},
 {"ID":"b","Project":"p","Service":"db","State":"exited","Name":"p-db-1"}
]`)
	got := ParsePs(arr)
	if len(got) != 2 || got[0].Name != "moodle" || got[0].State != "running" ||
		got[1].Name != "db" || got[1].State != "exited" {
		t.Fatalf("array shape: %+v", got)
	}

	// older: one object per line, lowercase keys, a name fallback
	stream := []byte(`{"service":"web","state":"running","health":"healthy"}` + "\n" +
		`{"name":"orphan","state":"restarting"}` + "\n")
	got = ParsePs(stream)
	if len(got) != 2 || got[0].Name != "web" || got[0].Health != "healthy" || got[1].Name != "orphan" {
		t.Fatalf("stream shape: %+v", got)
	}

	if got := ParsePs([]byte("")); len(got) != 0 {
		t.Fatalf("empty output must be no services: %+v", got)
	}
}

// What the ENV pane shows beyond a state: the image a service runs (always
// known), the published ports (only while something is exposed) and the exit
// code of a container that stopped. Compose types these differently between
// versions, and reports zero for the fields that do not apply.
func TestParsePsCarriesImagePortsAndExit(t *testing.T) {
	out := []byte(`[
	 {"Service":"web","State":"running","Image":"feicampus45-webserver",
	  "ExitCode":0,
	  "Publishers":[{"Published":8080,"Target":80,"Protocol":"tcp"},
	                {"Published":0,"Target":9000,"Protocol":"tcp"}]},
	 {"Service":"db","State":"exited","Image":"mariadb:11.4","ExitCode":137,"Publishers":[]},
	 {"Service":"admin","State":"running","Publishers":[{"Published":"3307","Target":"3306"}]}
	]`)
	got := ParsePs(out)
	if len(got) != 3 {
		t.Fatalf("services: %+v", got)
	}
	if got[0].Image != "feicampus45-webserver" {
		t.Fatalf("image: %+v", got[0])
	}
	if got[0].Ports != "8080→80" {
		t.Fatalf("ports (published 0 is not a port): %q", got[0].Ports)
	}
	if got[1].Exit != 137 || got[1].Ports != "" {
		t.Fatalf("stopped container: %+v", got[1])
	}
	// string-shaped numbers are the other shape compose has shipped
	if got[2].Ports != "3307→3306" {
		t.Fatalf("string ports: %q", got[2].Ports)
	}
}
