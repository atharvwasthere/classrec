package desktop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus    = kernel32.NewProc("GetSystemPowerStatus")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

// OnBattery reports whether the laptop is running on battery; false if Windows will not say.
func OnBattery() bool {
	var status struct {
		ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
		BatteryLifeTime, BatteryFullLifeTime                            uint32
	}
	ok, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	const offline = 0
	return ok != 0 && status.ACLineStatus == offline
}

// KeepAwake stops Windows sleeping or turning the screen off while on is true, which a class
// recording needs; it reports whether Windows accepted. Call it from one long-lived goroutine.
func KeepAwake(on bool) bool {
	const continuous, systemRequired, displayRequired = 0x80000000, 0x00000001, 0x00000002
	flags := uintptr(continuous)
	if on {
		flags |= systemRequired | displayRequired
	}
	previous, _, _ := procSetThreadExecutionState.Call(flags)
	return previous != 0
}

// FreeBytes is the space left on the drive holding path.
func FreeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, nil, nil)
	return free, err
}

// SingleInstance claims name for this process, so a second agent exits instead of fighting the
// first over OBS. The claim lasts until the process ends.
func SingleInstance(name string) error {
	n, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return err
	}
	_, err = windows.CreateMutex(nil, false, n)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return errors.New("another classrec agent is already running")
	}
	return err
}
