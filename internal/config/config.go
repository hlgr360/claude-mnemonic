// Package config provides configuration management for claude-mnemonic.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// DefaultWorkerPort is the default HTTP port for the worker service.
	DefaultWorkerPort = 37777

	// DefaultModel for SDK agent (use "haiku" for cost-efficient processing).
	// Claude Code CLI accepts aliases: haiku, sonnet, opus (always latest versions)
	DefaultModel = "haiku"
)

// DefaultObservationTypes are the observation types to include in context.
var DefaultObservationTypes = []string{
	"bugfix", "feature", "refactor", "change", "discovery", "decision",
}

// DefaultObservationConcepts are the concept tags to include in context.
var DefaultObservationConcepts = []string{
	"how-it-works", "why-it-exists", "what-changed",
	"problem-solution", "gotcha", "pattern", "trade-off",
}

// CriticalConcepts are concepts that indicate "must know" information.
// Observations with these concepts are prioritized in context injection.
var CriticalConcepts = []string{
	"gotcha", "pattern", "problem-solution", "trade-off",
}

// Config holds the application configuration.
// Field order optimized for memory alignment (fieldalignment).
type Config struct {
	ContextFullField             string   `json:"context_full_field"`
	DBPath                       string   `json:"db_path"`
	Model                        string   `json:"model"`
	ClaudeCodePath               string   `json:"claude_code_path"`
	EmbeddingModel               string   `json:"embedding_model"`
	VectorStorageStrategy        string   `json:"vector_storage_strategy"`
	OllamaURL                    string   `json:"ollama_url"`
	OllamaModel                  string   `json:"ollama_model"`
	OllamaKeepAlive              string   `json:"ollama_keep_alive"`
	LLMBackendSummary            string   `json:"llm_backend_summary"`
	LLMBackendObservation        string   `json:"llm_backend_observation"`
	LLMBackendVerify             string   `json:"llm_backend_verify"`
	LLMBackendBrief              string   `json:"llm_backend_brief"`
	LLMBackendConflict           string   `json:"llm_backend_conflict"`
	ContextObsConcepts           []string `json:"context_obs_concepts"`
	ContextObsTypes              []string `json:"context_obs_types"`
	ContextFullCount             int      `json:"context_full_count"`
	GraphBranchFactor            int      `json:"graph_branch_factor"`
	GraphEdgeWeight              float64  `json:"graph_edge_weight"`
	ContextRelevanceThreshold    float64  `json:"context_relevance_threshold"`
	RerankingCandidates          int      `json:"reranking_candidates"`
	WorkerPort                   int      `json:"worker_port"`
	OllamaNumCtx                 int      `json:"ollama_num_ctx"`
	OllamaTimeoutSeconds         int      `json:"ollama_timeout_seconds"`
	ConflictProposalsMinSim      float64  `json:"conflict_proposals_min_similarity"`
	GraphRelationsMinSim         float64  `json:"graph_relations_min_similarity"`
	GraphRelationsMaxPerObs      int      `json:"graph_relations_max_per_observation"`
	ProjectBriefMinNewObs        int      `json:"project_brief_min_new_observations"`
	ProjectBriefMaxAgeDays       int      `json:"project_brief_max_age_days"`
	ProjectBriefMaxPerRun        int      `json:"project_brief_max_per_run"`
	ProjectBriefIntervalMinutes  int      `json:"project_brief_interval_minutes"`
	ConflictProposalsMaxPerRun   int      `json:"conflict_proposals_max_per_run"`
	ConflictProposalsIntervalMin int      `json:"conflict_proposals_interval_minutes"`
	SupersededRetentionDays      int      `json:"superseded_retention_days"`
	DeduplicationThreshold       float64  `json:"deduplication_threshold"`
	RerankingMinImprovement      float64  `json:"reranking_min_improvement"`
	ContextObservations          int      `json:"context_observations"`
	ContextMaxPromptResults      int      `json:"context_max_prompt_results"`
	ContextSessionCount          int      `json:"context_session_count"`
	MaxConns                     int      `json:"max_conns"`
	RerankingAlpha               float64  `json:"reranking_alpha"`
	GraphMaxHops                 int      `json:"graph_max_hops"`
	RerankingResults             int      `json:"reranking_results"`
	GraphRebuildIntervalMin      int      `json:"graph_rebuild_interval_min"`
	HubThreshold                 int      `json:"hub_threshold"`
	ObservationRetentionDays     int      `json:"observation_retention_days"`
	// MaxObservationsPerProject caps the live notes of one project: the oldest beyond it are archived (never deleted).
	// 0 is no cap.
	MaxObservationsPerProject int `json:"max_observations_per_project"`
	// SnapshotIntervalHours is how often a regular snapshot of the database is taken while the worker runs; 0 is never.
	SnapshotIntervalHours int `json:"snapshot_interval_hours"`
	// SnapshotsDailyKeep is how many of those regular snapshots are kept.
	SnapshotsDailyKeep           int   `json:"snapshots_daily_keep"`
	MaintenanceIntervalHours     int   `json:"maintenance_interval_hours"`
	WALCheckpointIntervalSeconds int   `json:"wal_checkpoint_interval_seconds"`
	WALCheckpointThresholdBytes  int64 `json:"wal_checkpoint_threshold_bytes"`
	ContextMaxTokensStartup      int   `json:"context_max_tokens_startup"`
	ContextMaxTokensPrompt       int   `json:"context_max_tokens_prompt"`
	ContextShowWorkTokens        bool  `json:"context_show_work_tokens"`
	ContextShowReadTokens        bool  `json:"context_show_read_tokens"`
	RerankingPureMode            bool  `json:"reranking_pure_mode"`
	GraphEnabled                 bool  `json:"graph_enabled"`
	DeduplicationEnabled         bool  `json:"deduplication_enabled"`
	MaintenanceEnabled           bool  `json:"maintenance_enabled"`
	RerankingEnabled             bool  `json:"reranking_enabled"`
	ContextShowLastSummary       bool  `json:"context_show_last_summary"`
	CleanupStaleObservations     bool  `json:"cleanup_stale_observations"`
	LLMFallbackToClaude          bool  `json:"llm_fallback_to_claude"`
	ProjectBriefEnabled          bool  `json:"project_brief_enabled"`
	ConflictProposalsEnabled     bool  `json:"conflict_proposals_enabled"`
	// ProjectAutoMergeEnabled merges projects that are certainly one (the same git remote, the old folder gone) by itself.
	// Off by default: it only ever acts on the strongest evidence, with a backup and an alias, and says so.
	ProjectAutoMergeEnabled     bool `json:"project_auto_merge_enabled"`
	ProjectAutoMergeIntervalMin int  `json:"project_auto_merge_interval_minutes"`
}

var (
	globalConfig *Config
	configOnce   sync.Once
	configMu     sync.RWMutex
)

// DataDir returns the data directory path (~/.claude-mnemonic).
func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-mnemonic")
}

// DBPath returns the database file path.
func DBPath() string {
	return filepath.Join(DataDir(), "claude-mnemonic.db")
}

// SettingsPath returns the settings file path.
func SettingsPath() string {
	return filepath.Join(DataDir(), "settings.json")
}

// EnsureDataDir creates the data directory if it doesn't exist.
// Uses 0700 permissions (owner-only) for security.
func EnsureDataDir() error {
	return os.MkdirAll(DataDir(), 0700)
}

// EnsureSettings creates a default settings file if it doesn't exist.
func EnsureSettings() error {
	path := SettingsPath()

	// Check if file exists
	if _, err := os.Stat(path); err == nil {
		return nil // File exists
	}

	// Create default settings file with comments
	defaultSettings := `{
  "CLAUDE_MNEMONIC_WORKER_PORT": 37777,
  "CLAUDE_MNEMONIC_MODEL": "haiku",
  "CLAUDE_MNEMONIC_CONTEXT_OBSERVATIONS": 100,
  "CLAUDE_MNEMONIC_CONTEXT_FULL_COUNT": 25,
  "CLAUDE_MNEMONIC_CONTEXT_SESSION_COUNT": 10
}
`
	return os.WriteFile(path, []byte(defaultSettings), 0600)
}

// EnsureAll ensures all required directories and files exist.
func EnsureAll() error {
	if err := EnsureDataDir(); err != nil {
		return err
	}
	if err := EnsureSettings(); err != nil {
		return err
	}
	return nil
}

// LLM backends a task can run on.
const (
	BackendClaude = "claude"
	BackendOllama = "ollama"
)

// ValidBackend reports whether v names a known LLM backend.
func ValidBackend(v string) bool { return v == BackendClaude || v == BackendOllama }

// DefaultEmbeddingModel is the default embedding model to use.
const DefaultEmbeddingModel = "bge-v1.5"

// Default returns a Config with default values.
func Default() *Config {
	return &Config{
		WorkerPort:     DefaultWorkerPort,
		DBPath:         DBPath(),
		MaxConns:       4,
		Model:          DefaultModel,
		EmbeddingModel: DefaultEmbeddingModel,
		// Local LLM (Ollama) is opt-in: every task runs on the Claude CLI unless a backend is set.
		OllamaKeepAlive:       "10m",
		OllamaNumCtx:          16384, // room for a compaction excerpt plus the prompt
		OllamaTimeoutSeconds:  120,
		LLMBackendSummary:     BackendClaude,
		LLMBackendObservation: BackendClaude,
		LLMBackendVerify:      BackendClaude,
		LLMFallbackToClaude:   true, // an unreachable Ollama falls back to the CLI
		LLMBackendBrief:       BackendClaude,
		LLMBackendConflict:    BackendClaude,
		// Conflict proposals are on: a short Haiku call per new observation with close neighbours, at most
		// ConflictProposalsMaxPerRun per pass. They only ever propose: nothing is hidden or deleted without the
		// user's decision. Superseded notes are kept unless a retention is set.
		ConflictProposalsEnabled:     true,
		ProjectAutoMergeEnabled:      false,
		ProjectAutoMergeIntervalMin:  30,
		ConflictProposalsMaxPerRun:   20,
		ConflictProposalsIntervalMin: 60,
		ConflictProposalsMinSim:      0.65,
		// The knowledge graph links each observation to at most this many older, semantically close ones of its
		// project. Measured on a real archive: 0.6 and 3 give about 1.9 relations per note; the old all-pairs rules
		// gave about 29.
		GraphRelationsMinSim:    0.6,
		GraphRelationsMaxPerObs: 3,
		SupersededRetentionDays: 0,
		// Project briefs spend Claude usage, so they are opt-in. A brief is refreshed after enough new
		// observations, or when it is old and something is new.
		ProjectBriefEnabled:         false,
		ProjectBriefMinNewObs:       10,
		ProjectBriefMaxAgeDays:      7,
		ProjectBriefMaxPerRun:       3,
		ProjectBriefIntervalMinutes: 60,
		RerankingEnabled:            true,  // Enable by default for improved relevance
		RerankingCandidates:         100,   // Retrieve top 100 candidates
		RerankingResults:            10,    // Return top 10 after reranking
		RerankingAlpha:              0.7,   // Favor cross-encoder score
		RerankingMinImprovement:     0,     // Always apply reranking
		GraphEnabled:                true,  // Enable graph-aware search by default
		GraphMaxHops:                2,     // Two-hop traversal
		GraphBranchFactor:           5,     // Expand top 5 neighbors per node
		GraphEdgeWeight:             0.3,   // Minimum edge weight to follow
		GraphRebuildIntervalMin:     60,    // Rebuild graph every 60 minutes
		VectorStorageStrategy:       "hub", // Hub storage strategy (LEANN-inspired)
		HubThreshold:                5,     // Require 5+ accesses to store embedding
		ContextObservations:         100,
		ContextFullCount:            25,
		ContextSessionCount:         10,
		ContextShowReadTokens:       true,
		ContextShowWorkTokens:       true,
		ContextFullField:            "narrative",
		ContextShowLastSummary:      true,
		ContextObsTypes:             DefaultObservationTypes,
		ContextObsConcepts:          DefaultObservationConcepts,
		ContextRelevanceThreshold:   0.3,   // Minimum 30% similarity to include
		ContextMaxPromptResults:     10,    // Cap at 10 results max (0 = no cap, threshold only)
		ContextMaxTokensStartup:     16000, // Max tokens for SessionStart context injection
		ContextMaxTokensPrompt:      8000,  // Max tokens for UserPromptSubmit context injection
		DeduplicationEnabled:        true,  // Enable write-time vector dedup
		DeduplicationThreshold:      0.9,   // Similarity threshold for merging (0.9 = very similar)
		MaintenanceEnabled:          true,  // Enable scheduled maintenance
		MaintenanceIntervalHours:    6,     // Run every 6 hours
		ObservationRetentionDays:    0,     // 0 = no age-based deletion (keep all)
		MaxObservationsPerProject:   0,     // 0 = no cap; a cap archives the oldest notes, it never deletes
		SnapshotIntervalHours:       24,    // a snapshot at most once a day while the worker runs
		SnapshotsDailyKeep:          7,     // and the newest seven of them
		CleanupStaleObservations:    false, // Don't auto-cleanup stale observations
		// WAL checkpoint loop tunables (issue #49). Defaults mirror the worker constants:
		// check the WAL every 60s and TRUNCATE-checkpoint once it reaches 4 MiB.
		WALCheckpointIntervalSeconds: 60,
		WALCheckpointThresholdBytes:  4 << 20, // 4 MiB
	}
}

// Load loads configuration from the settings file, merging with defaults.
func Load() (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(SettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	// Load settings into a map to preserve unknown fields
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return cfg, nil // Return defaults on parse error
	}

	// Map settings to config
	if v, ok := settings["CLAUDE_MNEMONIC_WORKER_PORT"].(float64); ok {
		cfg.WorkerPort = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_MODEL"].(string); ok {
		cfg.Model = v
	}
	if v, ok := settings["CLAUDE_CODE_PATH"].(string); ok {
		cfg.ClaudeCodePath = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_EMBEDDING_MODEL"].(string); ok && v != "" {
		cfg.EmbeddingModel = v
	}
	// Local LLM (Ollama) settings
	if v, ok := settings["CLAUDE_MNEMONIC_OLLAMA_URL"].(string); ok {
		cfg.OllamaURL = strings.TrimSpace(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_OLLAMA_MODEL"].(string); ok {
		cfg.OllamaModel = strings.TrimSpace(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_OLLAMA_KEEP_ALIVE"].(string); ok && v != "" {
		cfg.OllamaKeepAlive = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_OLLAMA_NUM_CTX"].(float64); ok && v > 0 {
		cfg.OllamaNumCtx = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_OLLAMA_TIMEOUT_SECONDS"].(float64); ok && v > 0 {
		cfg.OllamaTimeoutSeconds = int(v)
	}
	for key, target := range map[string]*string{
		"CLAUDE_MNEMONIC_LLM_BACKEND_SUMMARY":     &cfg.LLMBackendSummary,
		"CLAUDE_MNEMONIC_LLM_BACKEND_OBSERVATION": &cfg.LLMBackendObservation,
		"CLAUDE_MNEMONIC_LLM_BACKEND_VERIFY":      &cfg.LLMBackendVerify,
		"CLAUDE_MNEMONIC_LLM_BACKEND_BRIEF":       &cfg.LLMBackendBrief,
		"CLAUDE_MNEMONIC_LLM_BACKEND_CONFLICT":    &cfg.LLMBackendConflict,
	} {
		if v, ok := settings[key].(string); ok && ValidBackend(strings.ToLower(strings.TrimSpace(v))) {
			*target = strings.ToLower(strings.TrimSpace(v))
		}
	}
	if v, ok := settings["CLAUDE_MNEMONIC_LLM_FALLBACK_TO_CLAUDE"].(bool); ok {
		cfg.LLMFallbackToClaude = v
	}
	// Project brief settings
	if v, ok := settings["CLAUDE_MNEMONIC_PROJECT_BRIEF_ENABLED"].(bool); ok {
		cfg.ProjectBriefEnabled = v
	}
	for key, target := range map[string]*int{
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MIN_NEW_OBSERVATIONS": &cfg.ProjectBriefMinNewObs,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_AGE_DAYS":         &cfg.ProjectBriefMaxAgeDays,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_PER_RUN":          &cfg.ProjectBriefMaxPerRun,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_INTERVAL_MINUTES":     &cfg.ProjectBriefIntervalMinutes,
	} {
		if v, ok := settings[key].(float64); ok && v > 0 {
			*target = int(v)
		}
	}
	// Conflict proposal settings
	if v, ok := settings["CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED"].(bool); ok {
		cfg.ConflictProposalsEnabled = v
	}
	for key, target := range map[string]*int{
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MAX_PER_RUN":      &cfg.ConflictProposalsMaxPerRun,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_INTERVAL_MINUTES": &cfg.ConflictProposalsIntervalMin,
	} {
		if v, ok := settings[key].(float64); ok && v > 0 {
			*target = int(v)
		}
	}
	if v, ok := settings["CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_ENABLED"].(bool); ok {
		cfg.ProjectAutoMergeEnabled = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_INTERVAL_MINUTES"].(float64); ok && v >= 1 {
		cfg.ProjectAutoMergeIntervalMin = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MIN_SIMILARITY"].(float64); ok && v > 0 && v <= 1 {
		cfg.ConflictProposalsMinSim = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_RELATIONS_MIN_SIMILARITY"].(float64); ok && v > 0 && v <= 1 {
		cfg.GraphRelationsMinSim = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_RELATIONS_MAX_PER_OBSERVATION"].(float64); ok && v >= 1 {
		cfg.GraphRelationsMaxPerObs = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_SUPERSEDED_RETENTION_DAYS"].(float64); ok && v >= 0 {
		cfg.SupersededRetentionDays = int(v)
	}
	// Reranking settings
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_ENABLED"].(bool); ok {
		cfg.RerankingEnabled = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_CANDIDATES"].(float64); ok && v > 0 {
		cfg.RerankingCandidates = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_RESULTS"].(float64); ok && v > 0 {
		cfg.RerankingResults = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_ALPHA"].(float64); ok && v >= 0 && v <= 1 {
		cfg.RerankingAlpha = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_MIN_IMPROVEMENT"].(float64); ok && v >= 0 {
		cfg.RerankingMinImprovement = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_RERANKING_PURE_MODE"].(bool); ok {
		cfg.RerankingPureMode = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_OBSERVATIONS"].(float64); ok {
		cfg.ContextObservations = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_MAX_OBSERVATIONS_PER_PROJECT"].(float64); ok && v >= 0 {
		cfg.MaxObservationsPerProject = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS"].(float64); ok && v >= 0 {
		cfg.SnapshotIntervalHours = int(v) // 0 switches the regular snapshot off
	}
	if v, ok := settings["CLAUDE_MNEMONIC_SNAPSHOTS_DAILY_KEEP"].(float64); ok && v > 0 {
		cfg.SnapshotsDailyKeep = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_FULL_COUNT"].(float64); ok {
		cfg.ContextFullCount = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_SESSION_COUNT"].(float64); ok {
		cfg.ContextSessionCount = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_OBS_TYPES"].(string); ok && v != "" {
		cfg.ContextObsTypes = splitTrim(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_OBS_CONCEPTS"].(string); ok && v != "" {
		cfg.ContextObsConcepts = splitTrim(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_RELEVANCE_THRESHOLD"].(float64); ok && v >= 0 && v <= 1 {
		cfg.ContextRelevanceThreshold = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_MAX_PROMPT_RESULTS"].(float64); ok && v >= 0 {
		cfg.ContextMaxPromptResults = int(v)
	}
	// Graph settings
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_ENABLED"].(bool); ok {
		cfg.GraphEnabled = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_MAX_HOPS"].(float64); ok && v > 0 {
		cfg.GraphMaxHops = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_BRANCH_FACTOR"].(float64); ok && v > 0 {
		cfg.GraphBranchFactor = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_EDGE_WEIGHT"].(float64); ok && v >= 0 && v <= 1 {
		cfg.GraphEdgeWeight = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_GRAPH_REBUILD_INTERVAL_MIN"].(float64); ok && v > 0 {
		cfg.GraphRebuildIntervalMin = int(v)
	}
	// Vector storage settings (LEANN Phase 2)
	if v, ok := settings["CLAUDE_MNEMONIC_VECTOR_STORAGE_STRATEGY"].(string); ok && v != "" {
		cfg.VectorStorageStrategy = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_HUB_THRESHOLD"].(float64); ok && v > 0 {
		cfg.HubThreshold = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_MAX_TOKENS_STARTUP"].(float64); ok && v > 0 {
		cfg.ContextMaxTokensStartup = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_CONTEXT_MAX_TOKENS_PROMPT"].(float64); ok && v > 0 {
		cfg.ContextMaxTokensPrompt = int(v)
	}
	// WAL checkpoint loop tunables (issue #49)
	if v, ok := settings["CLAUDE_MNEMONIC_WAL_CHECKPOINT_INTERVAL_SECONDS"].(float64); ok && v > 0 {
		cfg.WALCheckpointIntervalSeconds = int(v)
	}
	if v, ok := settings["CLAUDE_MNEMONIC_WAL_CHECKPOINT_THRESHOLD_BYTES"].(float64); ok && v > 0 {
		cfg.WALCheckpointThresholdBytes = int64(v)
	}
	// Deduplication settings
	if v, ok := settings["CLAUDE_MNEMONIC_DEDUP_ENABLED"].(bool); ok {
		cfg.DeduplicationEnabled = v
	}
	if v, ok := settings["CLAUDE_MNEMONIC_DEDUP_THRESHOLD"].(float64); ok && v > 0 && v <= 1 {
		cfg.DeduplicationThreshold = v
	}

	// Also support env vars for dedup settings
	if v := os.Getenv("CLAUDE_MNEMONIC_DEDUP_ENABLED"); v != "" {
		cfg.DeduplicationEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("CLAUDE_MNEMONIC_DEDUP_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			cfg.DeduplicationThreshold = f
		}
	}

	return cfg, nil
}

// splitTrim splits a comma-separated string and trims whitespace.
func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Get returns the global configuration, loading it if necessary.
func Get() *Config {
	configOnce.Do(func() {
		var err error
		globalConfig, err = Load()
		if err != nil {
			globalConfig = Default()
		}
	})

	configMu.RLock()
	defer configMu.RUnlock()
	return globalConfig
}

// GetWorkerPort returns the worker port from environment or config.
func GetWorkerPort() int {
	if port := os.Getenv("CLAUDE_MNEMONIC_WORKER_PORT"); port != "" {
		var p int
		if err := json.Unmarshal([]byte(port), &p); err == nil && p > 0 {
			return p
		}
	}
	return Get().WorkerPort
}
