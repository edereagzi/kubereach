package main

import "golang.org/x/sys/windows"

// A second launch asks the running instance to show its window, which Windows lets it bring to the front only when
// this process, the one the user just started, allows it. Wails signals the running instance in application.New.
func init() {
	const asfwAny = 0xFFFFFFFF
	_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow").Call(asfwAny)
}
