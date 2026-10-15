package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeUUID(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "11111111-1111-1111-1111-111111111111", want: "11111111-1111-1111-1111-111111111111"},
		{in: "11111111111111111111111111111111", want: "11111111-1111-1111-1111-111111111111"},
		{in: strings.ToUpper(testUUID), want: "11111111-1111-1111-1111-111111111111"},
		{in: "{11111111-1111-1111-1111-111111111111}", want: "11111111-1111-1111-1111-111111111111"},
		{in: "  11111111-1111-1111-1111-111111111111  ", want: "11111111-1111-1111-1111-111111111111"},
		{in: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", want: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{in: "", wantErr: true},
		{in: "not-a-uuid", wantErr: true},
		{in: "11111111-1111-1111-1111-11111111111", wantErr: true},   // 31 digits
		{in: "11111111-1111-1111-1111-1111111111111", wantErr: true}, // 33 digits
		{in: "gggggggg-gggg-gggg-gggg-gggggggggggg", wantErr: true},  // non-hex
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeUUID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeUUID(%q) = %q, want an error", tt.in, got)
				}
				if IsValidUUID(tt.in) {
					t.Errorf("IsValidUUID(%q) = true, want false", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeUUID(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeUUID(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !IsValidUUID(got) {
				t.Errorf("IsValidUUID(%q) = false, want true", got)
			}
		})
	}
}

func TestServerNormalize(t *testing.T) {
	t.Run("name falls back to host:port", func(t *testing.T) {
		srv := Server{Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}
		srv.Normalize()
		if srv.Name != testHost+":8443" {
			t.Errorf("Name = %q, want %q", srv.Name, testHost+":8443")
		}
	})

	t.Run("vless defaults", func(t *testing.T) {
		srv := Server{
			Name: "N", Type: "VLESS", Address: testHost, Port: 443,
			UUID:     strings.ToUpper(testUUID),
			Security: "REALITY", SNI: "www.example.com", PublicKey: testPBK,
		}
		srv.Normalize()
		if srv.Type != TypeVLESS {
			t.Errorf("Type = %q, want vless", srv.Type)
		}
		if srv.UUID != testUUID {
			t.Errorf("UUID = %q, want %q", srv.UUID, testUUID)
		}
		if srv.Security != SecurityReality {
			t.Errorf("Security = %q, want reality", srv.Security)
		}
		if srv.Network != "tcp" {
			t.Errorf("Network = %q, want tcp", srv.Network)
		}
		if srv.HeaderType != "none" {
			t.Errorf("HeaderType = %q, want none", srv.HeaderType)
		}
		if srv.Fingerprint != "chrome" {
			t.Errorf("Fingerprint = %q, want chrome", srv.Fingerprint)
		}
	})

	t.Run("plain tcp keeps an empty fingerprint", func(t *testing.T) {
		srv := Server{
			Name: "N", Type: TypeVLESS, Address: testHost, Port: 443, UUID: testUUID,
		}
		srv.Normalize()
		if srv.Fingerprint != "" {
			t.Errorf("Fingerprint = %q, want empty without tls/reality", srv.Fingerprint)
		}
	})

	t.Run("hysteria2 obfs=none is dropped and alpn is trimmed", func(t *testing.T) {
		srv := Server{
			Name: "N", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW,
			Obfs: "NONE", ALPN: []string{" h3 ", "", "h2"},
		}
		srv.Normalize()
		if srv.Obfs != "" {
			t.Errorf("Obfs = %q, want empty for the none default", srv.Obfs)
		}
		if !reflect.DeepEqual(srv.ALPN, []string{"h3", "h2"}) {
			t.Errorf("ALPN = %#v, want [h3 h2]", srv.ALPN)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		srv := Server{Type: TypeVLESS, Address: testHost, Port: 443, UUID: testUUID, Security: SecurityReality, SNI: "a.example.com", PublicKey: testPBK}
		srv.Normalize()
		once := srv
		srv.Normalize()
		if !reflect.DeepEqual(once, srv) {
			t.Errorf("Normalize is not idempotent:\n once: %#v\n twice: %#v", once, srv)
		}
	})
}

func TestServerValidate(t *testing.T) {
	validVLESS := func() Server {
		return Server{
			Name: "vless-node", Type: TypeVLESS, Address: testHost, Port: 443,
			UUID: testUUID, Security: SecurityReality, SNI: "www.example.com",
			PublicKey: testPBK, ShortID: testSID, Network: "tcp", HeaderType: "none",
			Fingerprint: "chrome",
		}
	}
	validHy2 := func() Server {
		return Server{
			Name: "hy2-node", Type: TypeHysteria2, Address: testHost, Port: 8443,
			Password: testPW,
		}
	}

	tests := []struct {
		name    string
		srv     Server
		wantSub string
	}{
		{name: "valid vless reality", srv: validVLESS()},
		{name: "valid hysteria2", srv: validHy2()},
		{name: "valid plain vless tcp", srv: func() Server {
			s := validVLESS()
			s.Security, s.SNI, s.PublicKey, s.ShortID, s.Fingerprint = "", "", "", "", ""
			return s
		}()},
		{name: "valid hysteria2 with obfs", srv: func() Server {
			s := validHy2()
			s.Obfs, s.ObfsPassword = "salamander", "cafebabe"
			return s
		}()},
		{name: "empty name", srv: func() Server {
			s := validHy2()
			s.Name = ""
			return s
		}(), wantSub: "name must not be empty"},
		{name: "name with newline", srv: func() Server {
			s := validHy2()
			s.Name = "two\nlines"
			return s
		}(), wantSub: "control characters"},
		{name: "unknown type", srv: func() Server {
			s := validHy2()
			s.Type = "trojan"
			return s
		}(), wantSub: "not one of vless, hysteria2"},
		{name: "empty server", srv: func() Server {
			s := validHy2()
			s.Address = ""
			return s
		}(), wantSub: "server must not be empty"},
		{name: "server with a slash", srv: func() Server {
			s := validHy2()
			s.Address = "example.com/path"
			return s
		}(), wantSub: "without spaces or slashes"},
		{name: "port zero", srv: func() Server {
			s := validHy2()
			s.Port = 0
			return s
		}(), wantSub: "out of range"},
		{name: "port too large", srv: func() Server {
			s := validHy2()
			s.Port = 65536
			return s
		}(), wantSub: "out of range"},
		{name: "vless without uuid", srv: func() Server {
			s := validVLESS()
			s.UUID = ""
			return s
		}(), wantSub: "uuid is required"},
		{name: "vless with bad uuid", srv: func() Server {
			s := validVLESS()
			s.UUID = "nope"
			return s
		}(), wantSub: "not a UUID"},
		{name: "reality without public key", srv: func() Server {
			s := validVLESS()
			s.PublicKey = ""
			return s
		}(), wantSub: "public_key"},
		{name: "reality without sni", srv: func() Server {
			s := validVLESS()
			s.SNI = ""
			return s
		}(), wantSub: "sni is required"},
		{name: "tls without sni", srv: func() Server {
			s := validVLESS()
			s.Security, s.PublicKey = SecurityTLS, ""
			s.SNI = ""
			return s
		}(), wantSub: "sni is required for vless with security=tls"},
		{name: "unknown security", srv: func() Server {
			s := validVLESS()
			s.Security = "xtls"
			return s
		}(), wantSub: "not one of none, tls, reality"},
		{name: "unsupported network", srv: func() Server {
			s := validVLESS()
			s.Network = "ws"
			return s
		}(), wantSub: "tcp only"},
		{name: "hysteria2 without password", srv: func() Server {
			s := validHy2()
			s.Password = ""
			return s
		}(), wantSub: "password is required"},
		{name: "obfs without obfs password", srv: func() Server {
			s := validHy2()
			s.Obfs = "salamander"
			return s
		}(), wantSub: "obfs_password is required"},
		{name: "obfs password without obfs", srv: func() Server {
			s := validHy2()
			s.ObfsPassword = "cafebabe"
			return s
		}(), wantSub: "obfs_password is set but obfs is empty"},
		{name: "vless ignores hysteria2 fields", srv: func() Server {
			s := validVLESS()
			s.ObfsPassword = "cafebabe"
			return s
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.srv.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("error type = %T, want *ValidationError", err)
			}
			if strings.Contains(err.Error(), testPW) || strings.Contains(err.Error(), testUUID) {
				t.Errorf("validation error leaks a secret: %q", err)
			}
		})
	}
}

func TestServerDetailAndEndpoint(t *testing.T) {
	vless := Server{
		Type: TypeVLESS, Address: testHost, Port: 443, Security: SecurityReality,
		Flow: "xtls-rprx-vision", Network: "tcp", SNI: "www.example.com",
	}
	if got := vless.Detail(); got != "reality/xtls-rprx-vision tcp sni=www.example.com" {
		t.Errorf("Detail() = %q", got)
	}
	if got := vless.Endpoint(); got != testHost+":443" {
		t.Errorf("Endpoint() = %q", got)
	}

	hy2 := Server{Type: TypeHysteria2, Address: "2001:db8::1", Port: 8443, Obfs: "salamander", SNI: "www.example.com", Insecure: true}
	if got := hy2.Endpoint(); got != "[2001:db8::1]:8443" {
		t.Errorf("Endpoint() = %q, want a bracketed IPv6 host", got)
	}
	if got := hy2.Detail(); got != "obfs=salamander sni=www.example.com insecure" {
		t.Errorf("Detail() = %q", got)
	}

	var nilSrv *Server
	if got := nilSrv.String(); got != "<nil>" {
		t.Errorf("String() on nil = %q", got)
	}
}

func TestServerSetAddDuplicateAndOverwrite(t *testing.T) {
	ss := NewServerSet()
	first := Server{Name: "node", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: "aaaa"}
	second := Server{Name: "NODE", Type: TypeHysteria2, Address: "198.51.100.7", Port: 443, Password: "bbbb"}
	third := Server{Name: "other", Type: TypeHysteria2, Address: "198.51.100.8", Port: 443, Password: "cccc"}

	if err := ss.Add(first, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ss.Add(third, false); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := ss.Add(second, false)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Add(duplicate) = %v, want ErrDuplicate", err)
	}

	if err := ss.Add(second, true); err != nil {
		t.Fatalf("Add(overwrite): %v", err)
	}
	if ss.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", ss.Len())
	}
	// Overwrite must replace in place, keeping list order.
	if ss.Servers[0].Name != "NODE" || ss.Servers[1].Name != "other" {
		t.Errorf("order after overwrite = %v, want [NODE other]", ss.Names())
	}
	if ss.Servers[0].Address != "198.51.100.7" {
		t.Errorf("overwrite did not replace the entry: %#v", ss.Servers[0])
	}
}

func TestServerSetAddValidates(t *testing.T) {
	ss := NewServerSet()
	// Missing password, and not normalized beforehand: Add must normalize and
	// then reject it.
	err := ss.Add(Server{Name: "broken", Type: TypeHysteria2, Address: testHost, Port: 8443}, false)
	if err == nil {
		t.Fatal("Add() = nil, want a validation error")
	}
	if ss.Len() != 0 {
		t.Errorf("Len() = %d, want 0 after a rejected add", ss.Len())
	}
	// The name fallback happens before validation, so the error mentions it.
	if !strings.Contains(err.Error(), testHost+":8443") && !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %q, want it to name the offending server", err)
	}
}

func TestServerSetGetIsCaseInsensitive(t *testing.T) {
	ss := NewServerSet()
	if err := ss.Add(Server{Name: "Node-A", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, name := range []string{"Node-A", "node-a", "NODE-A"} {
		if _, ok := ss.Get(name); !ok {
			t.Errorf("Get(%q) not found, want a case-insensitive match", name)
		}
	}
	if _, ok := ss.Get("missing"); ok {
		t.Error("Get(missing) found something")
	}
	if names := ss.Names(); !reflect.DeepEqual(names, []string{"Node-A"}) {
		t.Errorf("Names() = %v", names)
	}
	if found := ss.FindAll("node"); len(found) != 1 {
		t.Errorf("FindAll(node) = %d results, want 1", len(found))
	}
}

func TestServerSetRemove(t *testing.T) {
	ss := NewServerSet()
	for _, n := range []string{"a", "b", "c"} {
		srv := Server{Name: n, Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}
		if err := ss.Add(srv, false); err != nil {
			t.Fatalf("Add(%s): %v", n, err)
		}
	}
	if !ss.Remove("B") {
		t.Error("Remove(B) = false, want true for a case-insensitive match")
	}
	if names := ss.Names(); !reflect.DeepEqual(names, []string{"a", "c"}) {
		t.Errorf("Names() = %v, want [a c]", names)
	}
	if ss.Remove("b") {
		t.Error("Remove(b) = true on the second call, want false")
	}
}

func TestServerSetValidate(t *testing.T) {
	t.Run("duplicate names", func(t *testing.T) {
		ss := &ServerSet{Servers: []Server{
			{Name: "dup", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW},
			{Name: "DUP", Type: TypeHysteria2, Address: testHost, Port: 8444, Password: testPW},
		}}
		err := ss.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want a duplicate error")
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error = %q, want it to mention duplicate", err)
		}
	})

	t.Run("invalid entry", func(t *testing.T) {
		ss := &ServerSet{Servers: []Server{
			{Name: "ok", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW},
			{Name: "bad", Type: TypeHysteria2, Address: testHost, Port: 70000, Password: testPW},
		}}
		err := ss.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want an error")
		}
		if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("error = %q, want it to mention the port range", err)
		}
	})

	t.Run("empty set is valid", func(t *testing.T) {
		if err := NewServerSet().Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})
}
