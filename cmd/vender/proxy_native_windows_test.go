//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// Read-only: ordinary go test never changes the user's real WinINET settings.
func TestNativeWinInetSettingsCanBeRead(t *testing.T) {
	if _, err := (winInetAPI{}).Read(); err != nil {
		t.Fatal(err)
	}
}

func TestWinInetNativeLayout(t *testing.T) {
	optionSize, listSize := uintptr(16), uintptr(32)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		optionSize, listSize = 12, 20
	}
	if unsafe.Sizeof(perConnOption{}) != optionSize || unsafe.Sizeof(perConnList{}) != listSize {
		t.Fatal("incorrect WinINET union/list layout")
	}
}
