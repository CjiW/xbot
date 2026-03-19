package agent

import (
	"testing"
)

func TestBuildTriggerInfo_NilConfig(t *testing.T) {
	info := BuildTriggerInfo(0, nil, nil, nil, nil, "")

	if info.MaxTokens != 100000 {
		t.Errorf("MaxTokens = %d, want 100000", info.MaxTokens)
	}
	if info.GrowthRate != 0 {
		t.Errorf("GrowthRate = %v, want 0", info.GrowthRate)
	}
	if info.TokenHistory != nil {
		t.Errorf("TokenHistory = %v, want nil", info.TokenHistory)
	}
	if len(info.RecentTools) != 0 {
		t.Errorf("RecentTools = %v, want empty", info.RecentTools)
	}
}

func TestBuildTriggerInfo_NilProvider(t *testing.T) {
	cfg := &ContextManagerConfig{MaxContextTokens: 50000}
	info := BuildTriggerInfo(1, nil, nil, nil, cfg, "")

	if info.MaxTokens != 50000 {
		t.Errorf("MaxTokens = %d, want 50000", info.MaxTokens)
	}
	if info.GrowthRate != 0 {
		t.Errorf("GrowthRate = %v, want 0", info.GrowthRate)
	}
}

func TestBuildTriggerInfo_WithProvider(t *testing.T) {
	cfg := &ContextManagerConfig{MaxContextTokens: 128000}
	provider := NewTriggerInfoProvider()

	// 预先记录两个快照
	provider.GrowthTracker.Record(1, 10000)
	provider.GrowthTracker.Record(2, 12000)

	// 使用 iteration=3 以避免覆盖已有快照（Record 会先检查容量再 append）
	info := BuildTriggerInfo(3, nil, nil, provider, cfg, "")

	if info.MaxTokens != 128000 {
		t.Errorf("MaxTokens = %d, want 128000", info.MaxTokens)
	}
	if info.GrowthRate == 0 {
		t.Errorf("GrowthRate = %v, want non-zero", info.GrowthRate)
	}
	if len(info.TokenHistory) != 3 {
		t.Errorf("TokenHistory length = %d, want 3 (2 pre-recorded + 1 from BuildTriggerInfo)", len(info.TokenHistory))
	}
}

func TestBuildTriggerInfo_ToolsUsed(t *testing.T) {
	toolsUsed := []string{"Read", "Read", "Edit", "Shell", "Grep"}
	info := BuildTriggerInfo(1, nil, toolsUsed, nil, nil, "")

	if len(info.RecentTools) != 5 {
		t.Errorf("RecentTools length = %d, want 5", len(info.RecentTools))
	}
	if info.ToolPattern != PatternMixed {
		t.Errorf("ToolPattern = %d, want PatternMixed (%d)", info.ToolPattern, PatternMixed)
	}
}

func TestBuildTriggerInfo_ToolsUsed_Short(t *testing.T) {
	toolsUsed := []string{"Read"}
	info := BuildTriggerInfo(1, nil, toolsUsed, nil, nil, "")

	if len(info.RecentTools) != 1 {
		t.Errorf("RecentTools length = %d, want 1", len(info.RecentTools))
	}
	if info.RecentTools[0] != "Read" {
		t.Errorf("RecentTools[0] = %q, want %q", info.RecentTools[0], "Read")
	}
	if info.ToolPattern != PatternReadHeavy {
		t.Errorf("ToolPattern = %d, want PatternReadHeavy (%d)", info.ToolPattern, PatternReadHeavy)
	}
}

func TestBuildTriggerInfo_ZeroMaxTokens(t *testing.T) {
	cfg := &ContextManagerConfig{MaxContextTokens: 0}
	info := BuildTriggerInfo(1, nil, nil, nil, cfg, "")

	if info.MaxTokens != 100000 {
		t.Errorf("MaxTokens = %d, want 100000 (fallback from 0)", info.MaxTokens)
	}
}
