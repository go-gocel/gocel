package memory

import (
	"math"
	"sync"
	"time"
)

// DecayConfig configures the memory decay and forgetting mechanism.
// Decay gradually reduces the importance of archived sessions over time,
// and sessions that fall below the minimum threshold are automatically forgotten.
//
// DecayConfig 配置记忆衰减与遗忘机制：随时间推移逐渐降低归档会话的重要性，
// 低于最低阈值的会话会被自动遗忘。
type DecayConfig struct {
	// Enabled controls whether decay is active. Default false.
	Enabled bool `json:"enabled"`

	// HalfLifeHours is the number of hours after which a session's importance
	// is halved (assuming no access). Default 24.
	HalfLifeHours float64 `json:"half_life_hours"`

	// AccessBoost is the amount of importance added each time a session is
	// accessed via GetRecentSessions. Default 0.3.
	AccessBoost float64 `json:"access_boost"`

	// MinImportance is the threshold below which sessions are forgotten
	// (removed) during a decay sweep. Default 0.2.
	MinImportance float64 `json:"min_importance"`

	// CheckInterval controls how many interactions between decay sweeps.
	// Default 5 (sweep every 5 interactions).
	CheckInterval int `json:"check_interval"`

	// MaxDecayPerSweep limits how many sessions can be forgotten in one sweep
	// to prevent mass deletion. Default 3. 0 means no limit.
	MaxForgetPerSweep int `json:"max_forget_per_sweep"`
}

// DefaultDecayConfig returns sensible defaults for memory decay.
//
// DefaultDecayConfig 返回记忆衰减的合理默认值。
func DefaultDecayConfig() DecayConfig {
	return DecayConfig{
		Enabled:           false,
		HalfLifeHours:     24.0,
		AccessBoost:       0.3,
		MinImportance:     0.2,
		CheckInterval:     5,
		MaxForgetPerSweep: 3,
	}
}

// MemoryDecay drives the decay-and-forget mechanism on ArchiveMemory.
//
// MemoryDecay 在 ArchiveMemory 上驱动衰减-遗忘机制。
type MemoryDecay struct {
	cfg DecayConfig
	mu  sync.Mutex
}

// NewMemoryDecay creates a MemoryDecay with the given config.
//
// NewMemoryDecay 使用给定配置创建 MemoryDecay。
func NewMemoryDecay(cfg DecayConfig) *MemoryDecay {
	if cfg.HalfLifeHours <= 0 {
		cfg.HalfLifeHours = 24.0
	}
	if cfg.AccessBoost <= 0 {
		cfg.AccessBoost = 0.3
	}
	if cfg.MinImportance <= 0 {
		cfg.MinImportance = 0.2
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 5
	}
	if cfg.MaxForgetPerSweep <= 0 {
		cfg.MaxForgetPerSweep = 3
	}
	return &MemoryDecay{cfg: cfg}
}

// Config returns the decay configuration.
//
// Config 返回衰减配置。
func (d *MemoryDecay) Config() DecayConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// SetConfig updates the decay configuration.
//
// SetConfig 更新衰减配置。
func (d *MemoryDecay) SetConfig(cfg DecayConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = cfg
}

// decayFactor computes the importance multiplier after elapsed hours,
// using exponential decay: multiplier = 2^(-elapsed / halfLife).
func decayFactor(elapsedHours, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 {
		return 1.0
	}
	return math.Pow(0.5, elapsedHours/halfLifeHours)
}

// Sweep applies decay to all archived sessions and forgets those below threshold.
// Returns the number of sessions forgotten.
// It mutates the sessions slice in-place and returns the retained slice.
//
// Sweep 对所有归档会话应用衰减并遗忘低于阈值的会话，返回被遗忘的会话数；
// 它就地修改会话切片并返回保留的切片。
func (d *MemoryDecay) Sweep(sessions []ArchivedSession, now time.Time) []ArchivedSession {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	if !cfg.Enabled || len(sessions) == 0 {
		return sessions
	}

	halfLife := cfg.HalfLifeHours
	boost := cfg.AccessBoost
	minImp := cfg.MinImportance
	maxForget := cfg.MaxForgetPerSweep

	var retained []ArchivedSession
	forgetCount := 0

	for _, s := range sessions {
		// Compute elapsed time since last access (or creation if never accessed)
		lastAccess := s.LastAccessedAt
		if lastAccess.IsZero() {
			lastAccess = s.CreatedAt
		}
		elapsed := now.Sub(lastAccess).Hours()

		// Apply exponential decay
		factor := decayFactor(elapsed, halfLife)
		s.Importance *= factor

		// Apply access boost for each access
		if s.AccessCount > 0 {
			s.Importance += float64(s.AccessCount) * boost
		}

		// Clamp importance to [0, 2.0] range
		if s.Importance < 0 {
			s.Importance = 0
		} else if s.Importance > 2.0 {
			s.Importance = 2.0
		}

		// Forget if below threshold
		if s.Importance < minImp && (maxForget <= 0 || forgetCount < maxForget) {
			forgetCount++
			continue
		}

		// Reset access count after decay application
		s.AccessCount = 0
		retained = append(retained, s)
	}

	return retained
}

// ShouldSweep checks whether a decay sweep should be performed based on
// the interaction count and check interval.
//
// ShouldSweep 根据交互计数与检查间隔判断是否应执行一次衰减清扫。
func (d *MemoryDecay) ShouldSweep(interactionCount int) bool {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	if !cfg.Enabled || cfg.CheckInterval <= 0 {
		return false
	}
	return interactionCount > 0 && interactionCount%cfg.CheckInterval == 0
}
