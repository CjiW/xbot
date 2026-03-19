package agent

import (
	"math"
	"testing"
)

func TestCalculateDynamicThreshold_BoundaryCases(t *testing.T) {
	tests := []struct {
		name       string
		info       TriggerInfo
		wantMin    float64 // 期望最小值
		wantMax    float64 // 期望最大值
		wantExact  float64 // 如果已知精确值（可选）
		checkExact bool
	}{
		{
			name: "min_threshold_high_ratio_high_growth_ReadHeavy",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 9000,             // ratio = 0.9
				GrowthRate:    6000,             // > 5000 → growthFactor = -0.15
				ToolPattern:   PatternReadHeavy, // patternFactor = -0.05
			},
			wantExact:  0.5, // phaseFactor=0.5, growth=-0.15, pattern=-0.05 → 0.3, clamped to 0.5
			checkExact: true,
		},
		{
			name: "max_threshold_low_ratio_zero_growth_SubAgent",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 1000, // ratio = 0.1 < 0.5
				GrowthRate:    0,
				ToolPattern:   PatternSubAgent, // patternFactor = +0.05
			},
			wantExact:  0.85, // phaseFactor=0.85, growth=0, pattern=0.05 → 0.9, clamped to 0.85
			checkExact: true,
		},
		{
			name: "mid_threshold_ratio_0_6_growth_1000",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 6000,         // ratio = 0.6
				GrowthRate:    1000,         // growthFactor = -0.05 * 1000/2000 = -0.025
				ToolPattern:   PatternMixed, // patternFactor = 0
			},
			wantMin: 0.7, // phaseFactor ~0.775, + growth -0.025 = 0.75
			wantMax: 0.8,
		},
		{
			name: "zero_max_tokens",
			info: TriggerInfo{
				MaxTokens:     0,
				CurrentTokens: 5000,
				GrowthRate:    3000,
				ToolPattern:   PatternReadHeavy,
			},
			wantExact:  0.85,
			checkExact: true,
		},
		{
			name: "zero_current_tokens",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 0,
				GrowthRate:    0,
				ToolPattern:   PatternConversation,
			},
			wantExact:  0.85,
			checkExact: true,
		},
		{
			name: "all_zero_values",
			info: TriggerInfo{
				MaxTokens:     0,
				CurrentTokens: 0,
				GrowthRate:    0,
				ToolPattern:   PatternConversation,
			},
			wantExact:  0.85,
			checkExact: true,
		},
		{
			name: "ratio_just_below_0_5",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 4999, // ratio = 0.4999
				GrowthRate:    0,
				ToolPattern:   PatternConversation,
			},
			wantExact:  0.85,
			checkExact: true,
		},
		{
			name: "ratio_just_at_0_9",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 9000, // ratio = 0.9
				GrowthRate:    0,
				ToolPattern:   PatternConversation,
			},
			wantExact:  0.5,
			checkExact: true,
		},
		{
			name: "high_growth_just_over_5000",
			info: TriggerInfo{
				MaxTokens:     10000,
				CurrentTokens: 5000, // ratio = 0.5
				GrowthRate:    5001, // just over 5000
				ToolPattern:   PatternSubAgent,
			},
			wantMin: 0.5,
			wantMax: 0.85,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateDynamicThreshold(tt.info)
			if got < 0.5 || got > 0.85 {
				t.Errorf("calculateDynamicThreshold() = %v, out of range [0.5, 0.85]", got)
			}
			if tt.checkExact && math.Abs(got-tt.wantExact) > 0.001 {
				t.Errorf("calculateDynamicThreshold() = %v, want %v", got, tt.wantExact)
			}
			if !tt.checkExact {
				if tt.wantMin > 0 && got < tt.wantMin {
					t.Errorf("calculateDynamicThreshold() = %v, want >= %v", got, tt.wantMin)
				}
				if tt.wantMax > 0 && got > tt.wantMax {
					t.Errorf("calculateDynamicThreshold() = %v, want <= %v", got, tt.wantMax)
				}
			}
		})
	}
}

func TestDetectToolPattern_FrequencyBased(t *testing.T) {
	tests := []struct {
		name        string
		recentTools []string
		want        ToolCallPattern
	}{
		{
			name:        "empty_returns_conversation",
			recentTools: []string{},
			want:        PatternConversation,
		},
		{
			name:        "all_reads_returns_ReadHeavy",
			recentTools: []string{"Read", "Read", "Read", "Grep", "Read", "Read", "Glob", "Read", "Read", "Read"},
			want:        PatternReadHeavy,
		},
		{
			name:        "mostly_reads_over_70",
			recentTools: []string{"Read", "Read", "Read", "Read", "Read", "Read", "Read", "Edit", "Shell", "Read"},
			want:        PatternReadHeavy, // 8/10 = 0.8 > 0.7
		},
		{
			name:        "exactly_70_reads_is_not_ReadHeavy",
			recentTools: []string{"Read", "Read", "Read", "Read", "Read", "Read", "Read", "Edit", "Shell", "Unknown"},
			want:        PatternMixed, // 7/10 = 0.7, not > 0.7
		},
		{
			name:        "all_writes_returns_WriteHeavy",
			recentTools: []string{"Edit", "Shell", "Edit", "Write", "Edit", "Shell", "Edit", "Write", "Edit", "Shell"},
			want:        PatternWriteHeavy,
		},
		{
			name:        "mostly_writes_over_70",
			recentTools: []string{"Edit", "Edit", "Edit", "Edit", "Edit", "Edit", "Edit", "Shell", "Read", "Edit"},
			want:        PatternWriteHeavy, // 8/10 = 0.8 > 0.7
		},
		{
			name:        "subAgent_over_30",
			recentTools: []string{"SubAgent", "SubAgent", "SubAgent", "SubAgent", "Read", "Read", "Edit", "Shell", "Read", "Grep"},
			want:        PatternSubAgent, // 4/10 = 0.4 > 0.3
		},
		{
			name:        "subAgent_exactly_30_is_not_SubAgent",
			recentTools: []string{"SubAgent", "SubAgent", "SubAgent", "Read", "Read", "Read", "Edit", "Shell", "Read", "Grep"},
			want:        PatternMixed, // 3/10 = 0.3, not > 0.3
		},
		{
			name:        "mixed_read_and_write",
			recentTools: []string{"Read", "Edit", "Read", "Shell", "Read", "Edit"},
			want:        PatternMixed, // 3/6 read = 0.5, 3/6 write = 0.5
		},
		{
			name:        "mixed_slight_read_majority",
			recentTools: []string{"Read", "Read", "Read", "Read", "Read", "Edit", "Edit", "Shell", "Edit", "Edit"},
			want:        PatternMixed, // 5/10 read = 0.5, 5/10 write = 0.5
		},
		{
			name:        "unknown_tools_returns_conversation",
			recentTools: []string{"Foo", "Bar", "Baz"},
			want:        PatternConversation,
		},
		{
			name:        "single_read",
			recentTools: []string{"Read"},
			want:        PatternReadHeavy, // 1/1 = 1.0 > 0.7
		},
		{
			name:        "single_write",
			recentTools: []string{"Edit"},
			want:        PatternWriteHeavy, // 1/1 = 1.0 > 0.7
		},
		{
			name:        "single_subAgent",
			recentTools: []string{"SubAgent"},
			want:        PatternSubAgent, // 1/1 = 1.0 > 0.3
		},
		{
			name:        "single_unknown",
			recentTools: []string{"UnknownTool"},
			want:        PatternConversation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectToolPattern(tt.recentTools)
			if got != tt.want {
				t.Errorf("DetectToolPattern(%v) = %v, want %v", tt.recentTools, got, tt.want)
			}
		})
	}
}
