package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const keepLogsFor = 14 * 24 * time.Hour

// agentLog writes to stderr, to logs\agent-<date>.log next to classrec (a new file each day, kept
// two weeks), and while a class is on, to that class's own log as well.
type agentLog struct {
	mu    sync.Mutex // the overlay's thread logs too
	dir   string
	day   string
	file  *os.File
	class *os.File
}

func openAgentLog() (*agentLog, error) {
	l := &agentLog{dir: filepath.Join(exeDir(), "logs")}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return nil, err
	}
	pruneLogs(l.dir, time.Now())
	return l, nil
}

func (l *agentLog) printf(format string, a ...any) {
	now := time.Now()
	line := now.Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, a...) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprint(os.Stderr, line)
	if day := now.Format("2006-01-02"); day != l.day {
		if l.file != nil {
			l.file.Close()
		}
		l.day = day
		l.file, _ = os.OpenFile(filepath.Join(l.dir, "agent-"+day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	for _, f := range []*os.File{l.file, l.class} {
		if f != nil {
			_, _ = f.WriteString(line) // a log that cannot be written must not stop the recording
		}
	}
}

// startClass also sends every line to path until endClass.
func (l *agentLog) startClass(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.class == nil {
		l.class, _ = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
}

func (l *agentLog) endClass() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.class != nil {
		l.class.Close()
		l.class = nil
	}
}

func (l *agentLog) close() {
	l.endClass()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
	}
}

// pruneLogs deletes the agent's own day logs older than two weeks. Only logs: never a recording.
func pruneLogs(dir string, now time.Time) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		day, found := strings.CutPrefix(e.Name(), "agent-")
		day, isLog := strings.CutSuffix(day, ".log")
		t, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if found && isLog && err == nil && now.Sub(t) > keepLogsFor {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
