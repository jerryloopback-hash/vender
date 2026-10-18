package config

import "testing"

func TestWindowsInterfaceAliasesAllowSpacesAndChinese(t *testing.T) {
	for _, name := range []string{"Wi-Fi 2", "以太网 2", "vender tun"} {
		if invalidInterfaceName(name, "windows") {
			t.Errorf("rejected valid Windows alias %q", name)
		}
		if !invalidInterfaceName(name, "linux") {
			t.Errorf("accepted Linux alias with spaces %q", name)
		}
	}
	for _, name := range []string{"a\x00b", "a\nb", "a/b", `a\b`} {
		if !invalidInterfaceName(name, "windows") {
			t.Errorf("accepted invalid Windows alias %q", name)
		}
	}
}
