//go:build !windows

package discover

import (
	"fmt"
	"strconv"
	"strings"
)

// listeners lists listening TCP sockets via lsof, enriched with the runtime
// (ps) and working directory (lsof -d cwd).
func (d *Discoverer) listeners() ([]listener, error) {
	out, stderr, err := d.run.Run("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn")
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(stderr, "not found") {
			return nil, fmt.Errorf("lsof not found — install it (macOS ships it; Linux: apt/dnf install lsof)")
		}
		if strings.TrimSpace(out) == "" {
			return []listener{}, nil
		}
	}
	ls := parseLsofListeners(out)
	if len(ls) == 0 {
		return ls, nil
	}

	pids := uniquePIDs(ls)
	if psOut, _, err := d.run.Run("ps", "-o", "pid=,comm=", "-p", strings.Join(pids, ",")); err == nil {
		comm := parsePsComm(psOut)
		for i := range ls {
			if c, ok := comm[ls[i].PID]; ok {
				ls[i].Comm = c
			}
		}
	}
	if cwdOut, _, err := d.run.Run("lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(pids, ",")); err == nil {
		cwd := parseLsofCwd(cwdOut)
		for i := range ls {
			if c, ok := cwd[ls[i].PID]; ok {
				ls[i].Cwd = c
			}
		}
	}
	return ls, nil
}

func parseLsofListeners(out string) []listener {
	var res []listener
	seen := map[string]bool{}
	var pid int
	var comm string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			comm = ""
		case 'c':
			comm = line[1:]
		case 'n':
			port := portFromAddr(line[1:])
			if port == 0 {
				continue
			}
			key := strconv.Itoa(pid) + ":" + strconv.Itoa(port)
			if seen[key] {
				continue
			}
			seen[key] = true
			res = append(res, listener{Port: port, PID: pid, Comm: comm})
		}
	}
	return res
}

func portFromAddr(addr string) int {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0
	}
	return p
}

func parsePsComm(out string) map[int]string {
	m := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			m[pid] = strings.Join(f[1:], " ")
		}
	}
	return m
}

func parseLsofCwd(out string) map[int]string {
	m := map[int]string{}
	var pid int
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid != 0 {
				m[pid] = line[1:]
			}
		}
	}
	return m
}

func uniquePIDs(ls []listener) []string {
	seen := map[int]bool{}
	var out []string
	for _, l := range ls {
		if !seen[l.PID] {
			seen[l.PID] = true
			out = append(out, strconv.Itoa(l.PID))
		}
	}
	return out
}
