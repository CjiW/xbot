package tools

import "testing"

func TestSubAgentCapabilities_ToMap(t *testing.T) {
	caps := SubAgentCapabilities{
		Memory:      true,
		SendMessage: true,
		SpawnAgent:  false,
	}

	m := caps.ToMap()
	if !m["memory"] {
		t.Error("expected memory=true")
	}
	if !m["send_message"] {
		t.Error("expected send_message=true")
	}
	if m["spawn_agent"] {
		t.Error("expected spawn_agent=false (absent)")
	}
}

func TestCapabilitiesFromMap(t *testing.T) {
	m := map[string]bool{
		"memory":       true,
		"spawn_agent":  true,
		"send_message": false,
	}

	caps := CapabilitiesFromMap(m)
	if !caps.Memory {
		t.Error("expected Memory=true")
	}
	if !caps.SpawnAgent {
		t.Error("expected SpawnAgent=true")
	}
	if caps.SendMessage {
		t.Error("expected SendMessage=false")
	}
}

func TestCapabilitiesFromMap_Nil(t *testing.T) {
	caps := CapabilitiesFromMap(nil)
	if caps.Memory || caps.SendMessage || caps.SpawnAgent {
		t.Error("expected all capabilities false for nil map")
	}
}

func TestSubAgentCapabilities_RoundTrip(t *testing.T) {
	original := SubAgentCapabilities{
		Memory:      true,
		SendMessage: false,
		SpawnAgent:  true,
	}

	roundTripped := CapabilitiesFromMap(original.ToMap())
	if roundTripped != original {
		t.Errorf("round-trip failed: got %+v, want %+v", roundTripped, original)
	}
}
