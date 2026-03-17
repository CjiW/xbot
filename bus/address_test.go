package bus

import (
	"testing"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		input   string
		want    Address
		wantErr bool
	}{
		{
			input: "im://feishu/ou_xxx",
			want:  Address{Scheme: "im", Domain: "feishu", ID: "ou_xxx"},
		},
		{
			input: "im://feishu/oc_670cd0d6",
			want:  Address{Scheme: "im", Domain: "feishu", ID: "oc_670cd0d6"},
		},
		{
			input: "im://qq/12345",
			want:  Address{Scheme: "im", Domain: "qq", ID: "12345"},
		},
		{
			input: "agent://main",
			want:  Address{Scheme: "agent", Domain: "main", ID: ""},
		},
		{
			input: "agent://main/code-reviewer",
			want:  Address{Scheme: "agent", Domain: "main", ID: "code-reviewer"},
		},
		{
			input: "system://cron",
			want:  Address{Scheme: "system", Domain: "cron", ID: ""},
		},
		{
			input:   "invalid",
			wantErr: true,
		},
		{
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAddress(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseAddress(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddress(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseAddress(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAddressString(t *testing.T) {
	tests := []struct {
		addr Address
		want string
	}{
		{Address{Scheme: "im", Domain: "feishu", ID: "ou_xxx"}, "im://feishu/ou_xxx"},
		{Address{Scheme: "agent", Domain: "main"}, "agent://main"},
		{Address{Scheme: "agent", Domain: "main", ID: "code-reviewer"}, "agent://main/code-reviewer"},
		{Address{Scheme: "system", Domain: "cron"}, "system://cron"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.addr.String()
			if got != tt.want {
				t.Errorf("Address.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAddressRoundTrip(t *testing.T) {
	addrs := []string{
		"im://feishu/ou_xxx",
		"im://qq/12345",
		"agent://main",
		"agent://main/code-reviewer",
		"system://cron",
	}
	for _, s := range addrs {
		t.Run(s, func(t *testing.T) {
			addr, err := ParseAddress(s)
			if err != nil {
				t.Fatalf("ParseAddress(%q) error: %v", s, err)
			}
			if got := addr.String(); got != s {
				t.Errorf("roundtrip: %q → %v → %q", s, addr, got)
			}
		})
	}
}

func TestAddressPredicates(t *testing.T) {
	im := NewIMAddress("feishu", "ou_xxx")
	if !im.IsIM() {
		t.Error("expected IsIM() = true")
	}
	if im.IsAgent() || im.IsSystem() {
		t.Error("IM address should not be agent or system")
	}
	if im.ChannelName() != "feishu" {
		t.Errorf("ChannelName() = %q, want %q", im.ChannelName(), "feishu")
	}

	ag := NewAgentAddress("main/code-reviewer")
	if !ag.IsAgent() {
		t.Error("expected IsAgent() = true")
	}
	if ag.Domain != "main" || ag.ID != "code-reviewer" {
		t.Errorf("NewAgentAddress(\"main/code-reviewer\") = %v", ag)
	}
	if ag.ChannelName() != "agent" {
		t.Errorf("ChannelName() = %q, want %q", ag.ChannelName(), "agent")
	}

	sys := NewSystemAddress("cron")
	if !sys.IsSystem() {
		t.Error("expected IsSystem() = true")
	}

	zero := Address{}
	if !zero.IsZero() {
		t.Error("expected IsZero() = true for zero value")
	}
	if im.IsZero() {
		t.Error("expected IsZero() = false for non-zero address")
	}
}

func TestAddressFromChannelID(t *testing.T) {
	tests := []struct {
		channel string
		id      string
		want    Address
	}{
		{"feishu", "oc_xxx", Address{Scheme: "im", Domain: "feishu", ID: "oc_xxx"}},
		{"qq", "12345", Address{Scheme: "im", Domain: "qq", ID: "12345"}},
		{"agent", "main/cr", Address{Scheme: "agent", Domain: "main", ID: "cr"}},
		{"system", "cron", Address{Scheme: "system", Domain: "cron"}},
	}
	for _, tt := range tests {
		t.Run(tt.channel+"/"+tt.id, func(t *testing.T) {
			got := AddressFromChannelID(tt.channel, tt.id)
			if got != tt.want {
				t.Errorf("AddressFromChannelID(%q, %q) = %v, want %v", tt.channel, tt.id, got, tt.want)
			}
		})
	}
}
