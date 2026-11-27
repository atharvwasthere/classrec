package desktop

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Edge's tab names come from Windows' UI Automation, the accessibility interface screen readers
// use. It lists every tab of a window, not only the one showing, and Edge's name for a tab says
// when the tab is using the camera or microphone ("Meet - abc-defg-hij - Microphone recording").

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	oleaut32             = windows.NewLazySystemDLL("oleaut32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")

	clsidCUIAutomation = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201, Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation   = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a, Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
)

// Vtable slots, from UIAutomationClient.h.
const (
	slotRelease                   = 2
	slotElementFromHandle         = 6  // IUIAutomation
	slotCreatePropertyCond        = 23 // IUIAutomation
	slotFindAll                   = 6  // IUIAutomationElement
	slotCurrentName               = 23 // IUIAutomationElement
	slotArrayLength               = 3  // IUIAutomationElementArray
	slotArrayElement              = 4  // IUIAutomationElementArray
	clsctxInprocServer            = 1
	treeScopeDescendants          = 4
	propertyControlType           = 30003
	controlTypeTabItem            = 50019
	variantInt32           uint16 = 3
)

// com is a COM interface pointer.
type com unsafe.Pointer

// call invokes method slot of o's vtable and returns the HRESULT.
func call(o com, slot int, args ...uintptr) uintptr {
	vtable := *(**[32]uintptr)(o)
	result, _, _ := syscall.SyscallN(vtable[slot], append([]uintptr{uintptr(o)}, args...)...)
	return result
}

func release(o com) {
	if o != nil {
		call(o, slotRelease)
	}
}

// variant is an OLE VARIANT holding a 32-bit integer.
type variant struct {
	vt       uint16
	_        [3]uint16
	val      int32
	_padding int32
}

// tabNames lists the names of the tabs in a browser window, or nil if UI Automation cannot say.
func tabNames(hwnd windows.HWND) []string {
	// Each thread needs COM. A thread that already has it keeps its own mode; this call then fails
	// harmlessly, and is left unpaired because the thread may go on using COM.
	_ = windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)

	var automation com
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&automation))); hr != 0 {
		return nil
	}
	defer release(automation)

	var window, condition, found com
	if call(automation, slotElementFromHandle, uintptr(hwnd), uintptr(unsafe.Pointer(&window))) != 0 {
		return nil
	}
	defer release(window)
	isTab := variant{vt: variantInt32, val: controlTypeTabItem}
	if call(automation, slotCreatePropertyCond, propertyControlType, uintptr(unsafe.Pointer(&isTab)), uintptr(unsafe.Pointer(&condition))) != 0 {
		return nil
	}
	defer release(condition)
	if call(window, slotFindAll, treeScopeDescendants, uintptr(condition), uintptr(unsafe.Pointer(&found))) != 0 || found == nil {
		return nil
	}
	defer release(found)

	var count int32
	call(found, slotArrayLength, uintptr(unsafe.Pointer(&count)))
	names := make([]string, 0, count)
	for i := range count {
		var tab com
		if call(found, slotArrayElement, uintptr(i), uintptr(unsafe.Pointer(&tab))) != 0 {
			continue
		}
		var name *uint16
		if call(tab, slotCurrentName, uintptr(unsafe.Pointer(&name))) == 0 && name != nil {
			names = append(names, windows.UTF16PtrToString(name))
			procSysFreeString.Call(uintptr(unsafe.Pointer(name)))
		}
		release(tab)
	}
	return names
}
