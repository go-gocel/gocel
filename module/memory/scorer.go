package memory

import (
	"context"
	"math"
	"sync"

	"github.com/go-gocel/gocel/core/types"
)

// Scorer assigns an importance score to an interaction for eviction/retention decisions.
// Higher score = more important, should be retained longer.
//
// Scorer 为交互分配重要度评分，用于驱逐/保留决策。分数越高越重要，应保留更久。
type Scorer interface {
	// Score evaluates the importance of an interaction.
	// msgs: the messages in the interaction.
	// info: contextual information about the interaction's position in the history.
	Score(ctx context.Context, msgs []*types.Message, info *ScoreInfo) float64
}

// ScoreInfo provides context for scoring an interaction.
// ScoreInfo 提供对交互评分的上下文信息。
type ScoreInfo struct {
	InteractionIndex int     // position in the interactions list (0 = oldest)
	InteractionCount int     // total interactions currently stored
	RecencyRatio     float64 // 0 (oldest) to 1 (newest), normalized position
}

// DefaultScorer assigns scores based on role weight, recency, and token efficiency.
// Score = roleWeight * 0.40 + recencyBoost * 0.30 + tokenEfficiency * 0.20 + toolCallBonus * 0.10
//
// DefaultScorer 依据角色权重、新近度与 token 效率分配分数，评分公式见上。
type DefaultScorer struct {
	mu sync.RWMutex

	// RoleWeights assigns base importance by message role.
	// Default: system=1.0, user=0.8, assistant=0.6, tool=0.5
	RoleWeights map[types.Role]float64

	// RecencyStrength controls how much newer interactions are favored (0-1).
	// 0 = no recency bias, 1 = maximum recency bias. Default 0.7.
	RecencyStrength float64
}

// NewDefaultScorer creates a DefaultScorer with sensible defaults.
// NewDefaultScorer 以合理的默认值创建 DefaultScorer。
func NewDefaultScorer() *DefaultScorer {
	return &DefaultScorer{
		RoleWeights: map[types.Role]float64{
			types.RoleSystem:    1.0,
			types.RoleUser:      0.8,
			types.RoleAssistant: 0.6,
			types.RoleTool:      0.5,
		},
		RecencyStrength: 0.7,
	}
}

// Score computes the importance score for an interaction.
// Score 计算一次交互的重要度评分。
func (s *DefaultScorer) Score(_ context.Context, msgs []*types.Message, info *ScoreInfo) float64 {
	if len(msgs) == 0 {
		return 0
	}

	s.mu.RLock()
	weights := s.RoleWeights
	recencyStr := s.RecencyStrength
	s.mu.RUnlock()

	// 1. Role weight: average of max role weight in the interaction
	maxRoleWeight := 0.0
	totalTokens := 0
	hasToolCall := false
	hasToolResult := false
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if w, ok := weights[msg.Role]; ok && w > maxRoleWeight {
			maxRoleWeight = w
		}
		// Token efficiency: count characters as proxy
		totalTokens += len([]rune(msg.Content))
		if len(msg.ToolCalls) > 0 {
			hasToolCall = true
		}
		if msg.Role == types.RoleTool {
			hasToolResult = true
		}
	}
	roleScore := maxRoleWeight

	// 2. Recency: newer interactions get a boost
	recencyScore := 0.0
	if info != nil && info.InteractionCount > 1 {
		recencyScore = info.RecencyRatio * recencyStr
	}

	// 3. Token efficiency: penalize very long messages (low information density)
	// Use a sigmoid-like curve: score = 1 / (1 + e^(x-500)/200)
	// This gives ~0.5 at 500 chars, ~0.12 at 1000 chars, ~0.02 at 1500 chars
	tokenEff := 1.0
	if totalTokens > 100 {
		tokenEff = 1.0 / (1.0 + math.Exp(float64(totalTokens-500)/200.0))
	}

	// 4. Tool interaction bonus: interactions with both tool calls and tool results
	// are more valuable as they show the agent's reasoning chain
	toolBonus := 0.0
	if hasToolCall && hasToolResult {
		toolBonus = 1.0
	} else if hasToolCall || hasToolResult {
		toolBonus = 0.3
	}

	// Weighted combination
	score := roleScore*0.40 + recencyScore*0.30 + tokenEff*0.20 + toolBonus*0.10

	// Clamp to [0, 1]
	if score > 1.0 {
		score = 1.0
	}
	if score < 0 {
		score = 0
	}
	return score
}

// WithRoleWeight sets a custom weight for a role.
// WithRoleWeight 为指定角色设置自定义权重。
func (s *DefaultScorer) WithRoleWeight(role types.Role, weight float64) *DefaultScorer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RoleWeights[role] = weight
	return s
}

// WithRecencyStrength sets the recency bias strength.
// WithRecencyStrength 设置新近度偏置强度。
func (s *DefaultScorer) WithRecencyStrength(strength float64) *DefaultScorer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecencyStrength = strength
	return s
}
