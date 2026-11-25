package recorder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultOBSBin is where the OBS installer puts obs64.exe.
const DefaultOBSBin = `C:\Program Files\obs-studio\bin\64bit`

// OBSRunning reports whether obs64.exe is a running process. If the process list cannot be read it
// says yes, so a hiccup never starts a second OBS.
func OBSRunning() bool {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "obs64.exe") {
			return true
		}
	}
	return false
}

// LauncherFor returns a function that starts OBS from binDir minimized to the tray. OBS must run
// with its own bin folder as the working directory or it cannot find its data files.
// --disable-shutdown-check stops OBS asking about safe mode after it has crashed, a question nobody
// is there to answer. --disable-updater stops OBS's update prompt from covering the screen mid-class.
func LauncherFor(binDir string) func() error {
	return func() error {
		exe := filepath.Join(binDir, "obs64.exe")
		if _, err := os.Stat(exe); err != nil {
			return fmt.Errorf("obs is not at %s: %w", exe, err)
		}
		cmd := exec.Command(exe, "--minimize-to-tray", "--disable-shutdown-check", "--disable-updater")
		cmd.Dir = binDir
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}
