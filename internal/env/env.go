// Package env auto-detects docker-compose environments and reads their
// service state. Detection is a pure filesystem pass (cheap stats on the
// project and its repos' roots); the state is one `docker compose ps`
// invocation per environment, parsed leniently because compose's JSON
// shape has moved between versions (a pretty-printed array today, a
// stream of objects before).
package env

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileNames are the compose candidates, in the order a human would
// recognise: the classic override-style name first.
var FileNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

type Service struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
	Image  string `json:"image,omitempty"`
	Ports  string `json:"ports,omitempty"` // "8080→80", "" while nothing is published
	Exit   int    `json:"exit"`            // meaningful only for a stopped container
}

// Detect returns the first directory holding a compose file and the file
// itself. candidates is ordered by precedence (the project root before
// its repos, so the environment "belongs to" the project).
func Detect(candidates []string) (dir, file string) {
	for _, c := range candidates {
		for _, n := range FileNames {
			p := filepath.Join(c, n)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return c, p
			}
		}
	}
	return "", ""
}

// Available reports whether the docker CLI exists; no daemon probe (the
// status call is where a dead daemon surfaces, with its own message).
func Available() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// Status asks compose for every service of the environment, stopped
// ones included: "0 services" must not look identical to "all down".
func Status(ctx context.Context, composeFile string) ([]Service, error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile,
		"ps", "--format", "json", "--all")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return ParsePs(stdout.Bytes()), nil
}

// StatusTimeout bounds a call that can hang on a dead daemon socket.
const StatusTimeout = 15 * time.Second

// ParsePs reads compose's `ps --format json` output in both shapes it has
// shipped: one JSON array (possibly multi-line pretty-printed) or a
// stream of one-object-per-line values. Field names arrive capitalised
// ("Service", "State"); a lowercase variant is accepted too.
func ParsePs(data []byte) []Service {
	var raw []map[string]any
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			raw = decodeStream(trimmed)
		}
	} else {
		raw = decodeStream(trimmed)
	}
	var out []Service
	for _, m := range raw {
		svc := Service{
			Name:   field(m, "Service", "service", "Name", "name"),
			State:  field(m, "State", "state"),
			Health: field(m, "Health", "health"),
			Image:  field(m, "Image", "image"),
			Ports:  ports(m["Publishers"]),
			Exit:   number(m["ExitCode"]),
		}
		if svc.Name == "" {
			svc.Name = field(m, "Name", "name") // containers outside compose entries
		}
		if svc.Name == "" {
			continue
		}
		out = append(out, svc)
	}
	return out
}

// ports is the published side of the mapping, the only part a person acts on:
// "8080→80". Compose has shipped Published as a number and as a string, and
// reports 0 for a service nothing is exposed from -- and while every container
// is down there is nothing published at all.
func ports(v any) string {
	list, ok := v.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		pub := stringify(m["Published"])
		if pub == "" || pub == "0" {
			continue
		}
		if target := stringify(m["Target"]); target != "" {
			out = append(out, pub+"→"+target)
		} else {
			out = append(out, pub)
		}
	}
	return strings.Join(out, ", ")
}

// stringify and number read the fields compose has typed both ways.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.Itoa(int(t))
	}
	return ""
}

func number(v any) int {
	n, _ := strconv.Atoi(stringify(v))
	return n
}

func decodeStream(data []byte) []map[string]any {
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return out // io.EOF or trailing noise: whatever parsed is valid
		}
		out = append(out, m)
	}
}

func field(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
