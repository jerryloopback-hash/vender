//go:build windows

package windowsacl

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckDACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	userACE := "(A;;FA;;;" + user.User.Sid.String() + ")"
	systemACE := "(A;;FA;;;SY)"
	valid := "D:P" + userACE + systemACE
	for _, tt := range []struct {
		name, sddl string
		valid      bool
	}{
		{"private", valid, true},
		{"numeric SYSTEM SID", "D:P" + userACE + "(A;;FA;;;S-1-5-18)", true},
		{"empty", "D:", false},
		{"missing SYSTEM", "D:P" + userACE, user.User.Sid.Equals(system)},
		{"missing user", "D:P" + systemACE, user.User.Sid.Equals(system)},
		{"Everyone", valid + "(A;;FA;;;WD)", false},
		{"Users", valid + "(A;;FA;;;BU)", false},
		{"Authenticated Users", valid + "(A;;FA;;;AU)", false},
		{"Administrators", valid + "(A;;FA;;;BA)", false},
		{"unrelated account", valid + "(A;;FA;;;S-1-5-21-1-2-3-1234)", false},
		{"read only user", "D:P(A;;FR;;;" + user.User.Sid.String() + ")" + systemACE, false},
		{"read only SYSTEM", "D:P" + userACE + "(A;;FR;;;SY)", false},
		{"inherit only user", "D:P(A;OICIIO;FA;;;" + user.User.Sid.String() + ")" + systemACE, false},
		{"unexpected deny ACE", valid + "(D;;FA;;;WD)", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tt.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkDACL(sd, user.User.Sid, system); (err == nil) != tt.valid {
				t.Fatalf("checkDACL = %v, want valid=%v", err, tt.valid)
			}
		})
	}
	for _, present := range []bool{false, true} {
		sd, err := windows.NewSecurityDescriptor()
		if err != nil {
			t.Fatal(err)
		}
		if err := sd.SetDACL(nil, present, false); err != nil {
			t.Fatal(err)
		}
		if err := checkDACL(sd, user.User.Sid, system); err == nil {
			t.Errorf("accepted missing/null DACL (present=%v)", present)
		}
	}
}
