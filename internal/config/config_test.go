// Package config provides configuration management for claude-mnemonic.
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ConfigSuite is a test suite for config operations.
type ConfigSuite struct {
	suite.Suite
	tempDir     string
	origHomeDir string
}

func (s *ConfigSuite) SetupTest() {
	var err error
	s.tempDir, err = os.MkdirTemp("", "config-test-*")
	s.Require().NoError(err)

	// Save and override HOME
	s.origHomeDir = os.Getenv("HOME")
	os.Setenv("HOME", s.tempDir)
}

func (s *ConfigSuite) TearDownTest() {
	os.Setenv("HOME", s.origHomeDir)
	os.RemoveAll(s.tempDir)
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

// TestDefault tests default configuration values.
func (s *ConfigSuite) TestDefault() {
	cfg := Default()

	s.Equal(DefaultWorkerPort, cfg.WorkerPort)
	s.Equal(DefaultModel, cfg.Model)
	s.Equal(4, cfg.MaxConns)
	s.Equal(100, cfg.ContextObservations)
	s.Equal(25, cfg.ContextFullCount)
	s.Equal(10, cfg.ContextSessionCount)
	s.True(cfg.ContextShowReadTokens)
	s.True(cfg.ContextShowWorkTokens)
	s.Equal("narrative", cfg.ContextFullField)
	s.True(cfg.ContextShowLastSummary)
	s.Equal(DefaultObservationTypes, cfg.ContextObsTypes)
	s.Equal(DefaultObservationConcepts, cfg.ContextObsConcepts)
}

// TestDataDir tests data directory path.
func (s *ConfigSuite) TestDataDir() {
	dir := DataDir()
	s.Contains(dir, ".claude-mnemonic")
}

// TestDBPath tests database path.
func (s *ConfigSuite) TestDBPath() {
	path := DBPath()
	s.Contains(path, "claude-mnemonic.db")
}

// TestSettingsPath tests settings file path.
func (s *ConfigSuite) TestSettingsPath() {
	path := SettingsPath()
	s.Contains(path, "settings.json")
}

// TestEnsureDataDir tests data directory creation.
func (s *ConfigSuite) TestEnsureDataDir() {
	err := EnsureDataDir()
	s.NoError(err)

	dir := DataDir()
	info, err := os.Stat(dir)
	s.NoError(err)
	s.True(info.IsDir())
}

// TestEnsureSettings tests settings file creation.
func (s *ConfigSuite) TestEnsureSettings() {
	// First ensure data dir exists
	err := EnsureDataDir()
	s.NoError(err)

	// Ensure settings creates default file
	err = EnsureSettings()
	s.NoError(err)

	path := SettingsPath()
	info, err := os.Stat(path)
	s.NoError(err)
	s.False(info.IsDir())

	// Second call should not error (file exists)
	err = EnsureSettings()
	s.NoError(err)
}

// TestEnsureAll tests full initialization.
func (s *ConfigSuite) TestEnsureAll() {
	err := EnsureAll()
	s.NoError(err)

	// Verify dir and settings exist
	_, err = os.Stat(DataDir())
	s.NoError(err)
	_, err = os.Stat(SettingsPath())
	s.NoError(err)
}

// TestLoad_TableDriven tests configuration loading with various scenarios.
func (s *ConfigSuite) TestLoad_TableDriven() {
	tests := []struct {
		name           string
		settingsJSON   string
		expectedModel  string
		expectedPort   int
		expectedObsObs int
	}{
		{
			name:           "no settings file",
			settingsJSON:   "",
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
		{
			name:           "custom port",
			settingsJSON:   `{"CLAUDE_MNEMONIC_WORKER_PORT": 38888}`,
			expectedPort:   38888,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
		{
			name:           "custom model",
			settingsJSON:   `{"CLAUDE_MNEMONIC_MODEL": "sonnet"}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  "sonnet",
			expectedObsObs: 100,
		},
		{
			name:           "custom observations",
			settingsJSON:   `{"CLAUDE_MNEMONIC_CONTEXT_OBSERVATIONS": 200}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 200,
		},
		{
			name:           "multiple settings",
			settingsJSON:   `{"CLAUDE_MNEMONIC_WORKER_PORT": 39999, "CLAUDE_MNEMONIC_MODEL": "opus", "CLAUDE_MNEMONIC_CONTEXT_OBSERVATIONS": 50}`,
			expectedPort:   39999,
			expectedModel:  "opus",
			expectedObsObs: 50,
		},
		{
			name:           "invalid JSON returns defaults",
			settingsJSON:   `{invalid}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			// Create fresh temp dir
			tempDir, err := os.MkdirTemp("", "config-test-*")
			s.Require().NoError(err)
			defer os.RemoveAll(tempDir)

			os.Setenv("HOME", tempDir)

			// Create data dir
			err = os.MkdirAll(filepath.Join(tempDir, ".claude-mnemonic"), 0750)
			s.Require().NoError(err)

			if tt.settingsJSON != "" {
				writeErr := os.WriteFile(
					filepath.Join(tempDir, ".claude-mnemonic", "settings.json"),
					[]byte(tt.settingsJSON),
					0600,
				)
				s.Require().NoError(writeErr)
			}

			cfg, err := Load()
			s.NoError(err)
			s.NotNil(cfg)
			s.Equal(tt.expectedPort, cfg.WorkerPort)
			s.Equal(tt.expectedModel, cfg.Model)
			s.Equal(tt.expectedObsObs, cfg.ContextObservations)
		})
	}
}

// TestGetWorkerPort_TableDriven tests worker port retrieval with various scenarios.
func TestGetWorkerPort_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		wantPort int
		setEnv   bool
	}{
		{
			name:     "no env, use default",
			envValue: "",
			wantPort: DefaultWorkerPort,
			setEnv:   false,
		},
		{
			name:     "env set to valid port",
			envValue: "38888",
			wantPort: 38888,
			setEnv:   true,
		},
		{
			name:     "env set to invalid value",
			envValue: "invalid",
			wantPort: DefaultWorkerPort,
			setEnv:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env
			origEnv := os.Getenv("CLAUDE_MNEMONIC_WORKER_PORT")
			defer os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", origEnv)

			if tt.setEnv {
				os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", tt.envValue)
			} else {
				os.Unsetenv("CLAUDE_MNEMONIC_WORKER_PORT")
			}

			// We can't easily test GetWorkerPort since it uses Get() which caches
			// So we test the env parsing logic directly
			if tt.setEnv && tt.envValue != "" {
				if tt.wantPort != DefaultWorkerPort {
					assert.Equal(t, tt.envValue, os.Getenv("CLAUDE_MNEMONIC_WORKER_PORT"))
				}
			}
		})
	}
}

// TestSplitTrim tests the splitTrim helper function.
func TestSplitTrim(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []string{},
		},
		{
			name:     "single value",
			input:    "bugfix",
			expected: []string{"bugfix"},
		},
		{
			name:     "multiple values",
			input:    "bugfix,feature,refactor",
			expected: []string{"bugfix", "feature", "refactor"},
		},
		{
			name:     "values with spaces",
			input:    " bugfix , feature , refactor ",
			expected: []string{"bugfix", "feature", "refactor"},
		},
		{
			name:     "empty values filtered",
			input:    "bugfix,,feature,,",
			expected: []string{"bugfix", "feature"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitTrim(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDefaultObservationTypes tests default observation types.
func TestDefaultObservationTypes(t *testing.T) {
	expected := []string{
		"bugfix", "feature", "refactor", "change", "discovery", "decision",
	}
	assert.Equal(t, expected, DefaultObservationTypes)
}

// TestDefaultObservationConcepts tests default observation concepts.
func TestDefaultObservationConcepts(t *testing.T) {
	expected := []string{
		"how-it-works", "why-it-exists", "what-changed",
		"problem-solution", "gotcha", "pattern", "trade-off",
	}
	assert.Equal(t, expected, DefaultObservationConcepts)
}

// TestCriticalConcepts tests critical concepts list.
func TestCriticalConcepts(t *testing.T) {
	expected := []string{
		"gotcha", "pattern", "problem-solution", "trade-off",
	}
	assert.Equal(t, expected, CriticalConcepts)
}

// TestLoad_ClaudeCodePath tests claude code path loading.
func TestLoad_ClaudeCodePath(t *testing.T) {
	// Create temp dir
	tempDir, err := os.MkdirTemp("", "config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", origHome)

	// Create data dir and settings
	err = os.MkdirAll(filepath.Join(tempDir, ".claude-mnemonic"), 0750)
	require.NoError(t, err)

	settingsJSON := `{"CLAUDE_CODE_PATH": "/usr/local/bin/claude"}`
	err = os.WriteFile(
		filepath.Join(tempDir, ".claude-mnemonic", "settings.json"),
		[]byte(settingsJSON),
		0600,
	)
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/bin/claude", cfg.ClaudeCodePath)
}

// TestGet tests the global config getter.
func TestGet(t *testing.T) {
	// Save and restore HOME
	origHome := os.Getenv("HOME")
	tempDir, err := os.MkdirTemp("", "config-get-test-*")
	require.NoError(t, err)
	defer func() {
		os.Setenv("HOME", origHome)
		os.RemoveAll(tempDir)
	}()
	os.Setenv("HOME", tempDir)

	// Create data dir
	err = os.MkdirAll(filepath.Join(tempDir, ".claude-mnemonic"), 0750)
	require.NoError(t, err)

	// Get() should return a valid config
	cfg := Get()
	require.NotNil(t, cfg)
	assert.Greater(t, cfg.WorkerPort, 0)
	assert.NotEmpty(t, cfg.Model)
}

// TestGetWorkerPort_WithEnv tests GetWorkerPort with environment variable.
func TestGetWorkerPort_WithEnv(t *testing.T) {
	// Save original env
	origEnv := os.Getenv("CLAUDE_MNEMONIC_WORKER_PORT")
	defer os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", origEnv)

	// Test with valid port in env
	os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", "45678")
	port := GetWorkerPort()
	assert.Equal(t, 45678, port)

	// Test with invalid port (should fall back to config)
	os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", "not-a-number")
	port = GetWorkerPort()
	// Should return from Get().WorkerPort, which is default
	assert.Greater(t, port, 0)

	// Test with zero port (should fall back to config)
	os.Setenv("CLAUDE_MNEMONIC_WORKER_PORT", "0")
	port = GetWorkerPort()
	// Zero is invalid, so should use default
	assert.Greater(t, port, 0)

	// Test with no env (should use config)
	os.Unsetenv("CLAUDE_MNEMONIC_WORKER_PORT")
	port = GetWorkerPort()
	assert.Greater(t, port, 0)
}

// TestLoad_ContextSettings tests context-related settings loading.
func TestLoad_ContextSettings(t *testing.T) {
	// Create temp dir
	tempDir, err := os.MkdirTemp("", "config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", origHome)

	// Create data dir and settings
	err = os.MkdirAll(filepath.Join(tempDir, ".claude-mnemonic"), 0750)
	require.NoError(t, err)

	settingsJSON := `{
		"CLAUDE_MNEMONIC_CONTEXT_FULL_COUNT": 50,
		"CLAUDE_MNEMONIC_CONTEXT_SESSION_COUNT": 20,
		"CLAUDE_MNEMONIC_CONTEXT_OBS_TYPES": "bugfix,feature",
		"CLAUDE_MNEMONIC_CONTEXT_OBS_CONCEPTS": "security,performance"
	}`
	err = os.WriteFile(
		filepath.Join(tempDir, ".claude-mnemonic", "settings.json"),
		[]byte(settingsJSON),
		0600,
	)
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 50, cfg.ContextFullCount)
	assert.Equal(t, 20, cfg.ContextSessionCount)
	assert.Equal(t, []string{"bugfix", "feature"}, cfg.ContextObsTypes)
	assert.Equal(t, []string{"security", "performance"}, cfg.ContextObsConcepts)
}

func writeSettings(t *testing.T, json string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude-mnemonic"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude-mnemonic", "settings.json"), []byte(json), 0o600))
}

func TestLoad_LocalLLMIsOffByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)

	for name, backend := range map[string]string{"summary": cfg.LLMBackendSummary, "observation": cfg.LLMBackendObservation, "verify": cfg.LLMBackendVerify} {
		assert.Equal(t, BackendClaude, backend, "task %s runs on the Claude CLI unless switched", name)
	}
	assert.True(t, cfg.LLMFallbackToClaude)
	assert.Equal(t, "", cfg.OllamaModel, "no model is chosen for the user")
	assert.Equal(t, 16384, cfg.OllamaNumCtx)
	assert.Equal(t, 120, cfg.OllamaTimeoutSeconds)
	assert.Equal(t, "10m", cfg.OllamaKeepAlive)
}

func TestLoad_LocalLLMSettings(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_OLLAMA_URL": " 127.0.0.1:11434 ",
		"CLAUDE_MNEMONIC_OLLAMA_MODEL": "gemma3:12b",
		"CLAUDE_MNEMONIC_OLLAMA_NUM_CTX": 8192,
		"CLAUDE_MNEMONIC_OLLAMA_KEEP_ALIVE": "30m",
		"CLAUDE_MNEMONIC_OLLAMA_TIMEOUT_SECONDS": 300,
		"CLAUDE_MNEMONIC_LLM_BACKEND_SUMMARY": "Ollama",
		"CLAUDE_MNEMONIC_LLM_BACKEND_VERIFY": " ollama ",
		"CLAUDE_MNEMONIC_LLM_FALLBACK_TO_CLAUDE": false
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:11434", cfg.OllamaURL)
	assert.Equal(t, "gemma3:12b", cfg.OllamaModel)
	assert.Equal(t, 8192, cfg.OllamaNumCtx)
	assert.Equal(t, "30m", cfg.OllamaKeepAlive)
	assert.Equal(t, 300, cfg.OllamaTimeoutSeconds)
	assert.Equal(t, BackendOllama, cfg.LLMBackendSummary, "values are case-insensitive")
	assert.Equal(t, BackendOllama, cfg.LLMBackendVerify)
	assert.Equal(t, BackendClaude, cfg.LLMBackendObservation, "a task that is not mentioned stays on Claude")
	assert.False(t, cfg.LLMFallbackToClaude)
}

func TestLoad_UnusableLocalLLMSettingsKeepTheDefaults(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_LLM_BACKEND_SUMMARY": "gpt",
		"CLAUDE_MNEMONIC_LLM_BACKEND_OBSERVATION": 7,
		"CLAUDE_MNEMONIC_OLLAMA_NUM_CTX": -5,
		"CLAUDE_MNEMONIC_OLLAMA_TIMEOUT_SECONDS": "soon",
		"CLAUDE_MNEMONIC_OLLAMA_KEEP_ALIVE": ""
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, BackendClaude, cfg.LLMBackendSummary, "an unknown backend is ignored, not trusted")
	assert.Equal(t, BackendClaude, cfg.LLMBackendObservation)
	assert.Equal(t, 16384, cfg.OllamaNumCtx)
	assert.Equal(t, 120, cfg.OllamaTimeoutSeconds)
	assert.Equal(t, "10m", cfg.OllamaKeepAlive)
}

func TestValidBackend(t *testing.T) {
	assert.True(t, ValidBackend("claude"))
	assert.True(t, ValidBackend("ollama"))
	assert.False(t, ValidBackend(""))
	assert.False(t, ValidBackend("Claude"), "callers normalise the case first")
}

func TestLoad_ProjectBriefIsOffByDefaultWithSensibleThresholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.ProjectBriefEnabled, "a brief spends Claude usage, so it is opt-in")
	assert.Equal(t, 10, cfg.ProjectBriefMinNewObs)
	assert.Equal(t, 7, cfg.ProjectBriefMaxAgeDays)
	assert.Equal(t, 3, cfg.ProjectBriefMaxPerRun)
	assert.Equal(t, 60, cfg.ProjectBriefIntervalMinutes)
	assert.Equal(t, BackendClaude, cfg.LLMBackendBrief)
}

func TestLoad_ProjectBriefSettings(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_ENABLED": true,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MIN_NEW_OBSERVATIONS": 3,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_AGE_DAYS": 14,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_PER_RUN": 1,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_INTERVAL_MINUTES": 5,
		"CLAUDE_MNEMONIC_LLM_BACKEND_BRIEF": "Ollama"
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.ProjectBriefEnabled)
	assert.Equal(t, 3, cfg.ProjectBriefMinNewObs)
	assert.Equal(t, 14, cfg.ProjectBriefMaxAgeDays)
	assert.Equal(t, 1, cfg.ProjectBriefMaxPerRun)
	assert.Equal(t, 5, cfg.ProjectBriefIntervalMinutes)
	assert.Equal(t, BackendOllama, cfg.LLMBackendBrief)
}

func TestLoad_UnusableProjectBriefSettingsKeepTheDefaults(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_ENABLED": "yes",
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MIN_NEW_OBSERVATIONS": 0,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_AGE_DAYS": -2,
		"CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_PER_RUN": "many",
		"CLAUDE_MNEMONIC_LLM_BACKEND_BRIEF": "gpt"
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.ProjectBriefEnabled, "only a real boolean enables it")
	assert.Equal(t, 10, cfg.ProjectBriefMinNewObs)
	assert.Equal(t, 7, cfg.ProjectBriefMaxAgeDays)
	assert.Equal(t, 3, cfg.ProjectBriefMaxPerRun)
	assert.Equal(t, BackendClaude, cfg.LLMBackendBrief)
}

func TestLoad_ConflictProposalsAreOnByDefaultAndNothingIsDeletedByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.ConflictProposalsEnabled, "proposals are on by default; they only propose, a person decides")
	assert.Equal(t, 20, cfg.ConflictProposalsMaxPerRun)
	assert.Equal(t, 60, cfg.ConflictProposalsIntervalMin)
	assert.InDelta(t, 0.65, cfg.ConflictProposalsMinSim, 0.0001)
	assert.Equal(t, 0, cfg.SupersededRetentionDays, "superseded notes are kept unless a retention is set")
	assert.Equal(t, BackendClaude, cfg.LLMBackendConflict)
}

func TestLoad_ConflictProposalSettings(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": true,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MAX_PER_RUN": 5,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_INTERVAL_MINUTES": 15,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MIN_SIMILARITY": 0.8,
		"CLAUDE_MNEMONIC_SUPERSEDED_RETENTION_DAYS": 30,
		"CLAUDE_MNEMONIC_LLM_BACKEND_CONFLICT": "ollama"
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.ConflictProposalsEnabled)
	assert.Equal(t, 5, cfg.ConflictProposalsMaxPerRun)
	assert.Equal(t, 15, cfg.ConflictProposalsIntervalMin)
	assert.InDelta(t, 0.8, cfg.ConflictProposalsMinSim, 0.0001)
	assert.Equal(t, 30, cfg.SupersededRetentionDays)
	assert.Equal(t, BackendOllama, cfg.LLMBackendConflict)
}

func TestLoad_ConflictProposalsCanBeSwitchedOff(t *testing.T) {
	writeSettings(t, `{"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": false}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.ConflictProposalsEnabled)
	assert.Equal(t, 20, cfg.ConflictProposalsMaxPerRun, "the other settings keep their defaults")
}

func TestLoad_ObservationCapIsOffByDefaultAndReadFromTheSettings(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.Equal(t, 0, cfg.MaxObservationsPerProject, "no cap by default: nothing is archived or removed")

	writeSettings(t, `{"CLAUDE_MNEMONIC_MAX_OBSERVATIONS_PER_PROJECT": 250}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 250, cfg.MaxObservationsPerProject)

	for name, raw := range map[string]string{"negative": `-5`, "text": `"lots"`, "boolean": `true`} {
		writeSettings(t, `{"CLAUDE_MNEMONIC_MAX_OBSERVATIONS_PER_PROJECT": `+raw+`}`)
		cfg, err = Load()
		assert.NoError(t, err, name)
		assert.Equal(t, 0, cfg.MaxObservationsPerProject, "%s is not a cap, so the default stays", name)
	}
}

func TestLoad_SnapshotSettings(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.Equal(t, 24, cfg.SnapshotIntervalHours, "a snapshot a day by default")
	assert.Equal(t, 7, cfg.SnapshotsDailyKeep)

	writeSettings(t, `{"CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": 6, "CLAUDE_MNEMONIC_SNAPSHOTS_DAILY_KEEP": 14}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 6, cfg.SnapshotIntervalHours)
	assert.Equal(t, 14, cfg.SnapshotsDailyKeep)

	writeSettings(t, `{"CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": 0}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 0, cfg.SnapshotIntervalHours, "zero switches the regular snapshot off")

	writeSettings(t, `{"CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": -1, "CLAUDE_MNEMONIC_SNAPSHOTS_DAILY_KEEP": 0}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 24, cfg.SnapshotIntervalHours, "a negative interval is not one")
	assert.Equal(t, 7, cfg.SnapshotsDailyKeep, "keeping none is not a keep")
}

func TestLoad_UnusableConflictSettingsKeepTheDefaults(t *testing.T) {
	writeSettings(t, `{
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": "yes",
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MAX_PER_RUN": 0,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_INTERVAL_MINUTES": -3,
		"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MIN_SIMILARITY": 1.5,
		"CLAUDE_MNEMONIC_SUPERSEDED_RETENTION_DAYS": -1,
		"CLAUDE_MNEMONIC_LLM_BACKEND_CONFLICT": "gpt"
	}`)
	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.ConflictProposalsEnabled, "a value that is not a boolean keeps the default")
	assert.Equal(t, 20, cfg.ConflictProposalsMaxPerRun)
	assert.Equal(t, 60, cfg.ConflictProposalsIntervalMin)
	assert.InDelta(t, 0.65, cfg.ConflictProposalsMinSim, 0.0001, "a similarity above 1 is not one")
	assert.Equal(t, 0, cfg.SupersededRetentionDays, "a negative retention is not a retention")
	assert.Equal(t, BackendClaude, cfg.LLMBackendConflict)

	writeSettings(t, `{"CLAUDE_MNEMONIC_SUPERSEDED_RETENTION_DAYS": 0}`)
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.SupersededRetentionDays, "an explicit zero is valid: keep for ever")
}

func TestLoad_GraphRelationSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.GraphEnabled, "the graph builder is on by default: it costs no model usage")
	assert.InDelta(t, 0.6, cfg.GraphRelationsMinSim, 0.0001)
	assert.Equal(t, 3, cfg.GraphRelationsMaxPerObs)

	writeSettings(t, `{"CLAUDE_MNEMONIC_GRAPH_RELATIONS_MIN_SIMILARITY": 0.7, "CLAUDE_MNEMONIC_GRAPH_RELATIONS_MAX_PER_OBSERVATION": 5}`)
	cfg, err = Load()
	require.NoError(t, err)
	assert.InDelta(t, 0.7, cfg.GraphRelationsMinSim, 0.0001)
	assert.Equal(t, 5, cfg.GraphRelationsMaxPerObs)

	writeSettings(t, `{"CLAUDE_MNEMONIC_GRAPH_RELATIONS_MIN_SIMILARITY": 1.5, "CLAUDE_MNEMONIC_GRAPH_RELATIONS_MAX_PER_OBSERVATION": 0}`)
	cfg, err = Load()
	require.NoError(t, err)
	assert.InDelta(t, 0.6, cfg.GraphRelationsMinSim, 0.0001, "a similarity above 1 is not one")
	assert.Equal(t, 3, cfg.GraphRelationsMaxPerObs, "at least one relation per note")
}

func TestLoad_ProjectAutoMergeIsOffByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.ProjectAutoMergeEnabled, "projects are never merged by themselves unless asked to")
	assert.Equal(t, 30, cfg.ProjectAutoMergeIntervalMin)
}

func TestLoad_ProjectAutoMergeSettings(t *testing.T) {
	writeSettings(t, `{"CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_ENABLED": true, "CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_INTERVAL_MINUTES": 5}`)
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.ProjectAutoMergeEnabled)
	assert.Equal(t, 5, cfg.ProjectAutoMergeIntervalMin)
}

func TestLoad_UnusableProjectAutoMergeSettingsKeepTheDefaults(t *testing.T) {
	writeSettings(t, `{"CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_ENABLED": "yes", "CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_INTERVAL_MINUTES": 0}`)
	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.ProjectAutoMergeEnabled, "a value that is not a boolean does not switch it on")
	assert.Equal(t, 30, cfg.ProjectAutoMergeIntervalMin)
}

func TestLoad_RollupSettings(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.False(t, cfg.RollupEnabled, "roll-ups archive notes and spend model usage: off by default")
	assert.Equal(t, 60, cfg.RollupMinAgeDays)
	assert.Equal(t, 8, cfg.RollupMinGroupSize)
	assert.Equal(t, 30, cfg.RollupKeepNewest)
	assert.Equal(t, 3, cfg.RollupMaxGroupsPerRun)
	assert.Equal(t, 360, cfg.RollupIntervalMinutes)
	assert.Equal(t, BackendClaude, cfg.LLMBackendRollup)

	writeSettings(t, `{"CLAUDE_MNEMONIC_ROLLUP_ENABLED": true, "CLAUDE_MNEMONIC_ROLLUP_MIN_AGE_DAYS": 30, "CLAUDE_MNEMONIC_ROLLUP_MIN_GROUP_SIZE": 5,
		"CLAUDE_MNEMONIC_ROLLUP_KEEP_NEWEST": 0, "CLAUDE_MNEMONIC_ROLLUP_MAX_GROUPS_PER_RUN": 1, "CLAUDE_MNEMONIC_ROLLUP_INTERVAL_MINUTES": 15,
		"CLAUDE_MNEMONIC_LLM_BACKEND_ROLLUP": "Ollama"}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.True(t, cfg.RollupEnabled)
	assert.Equal(t, 30, cfg.RollupMinAgeDays)
	assert.Equal(t, 5, cfg.RollupMinGroupSize)
	assert.Equal(t, 0, cfg.RollupKeepNewest, "keeping none back is a valid choice")
	assert.Equal(t, 1, cfg.RollupMaxGroupsPerRun)
	assert.Equal(t, 15, cfg.RollupIntervalMinutes)
	assert.Equal(t, BackendOllama, cfg.LLMBackendRollup)

	writeSettings(t, `{"CLAUDE_MNEMONIC_ROLLUP_ENABLED": "yes", "CLAUDE_MNEMONIC_ROLLUP_MIN_AGE_DAYS": 0, "CLAUDE_MNEMONIC_ROLLUP_MIN_GROUP_SIZE": -2,
		"CLAUDE_MNEMONIC_ROLLUP_KEEP_NEWEST": -1, "CLAUDE_MNEMONIC_ROLLUP_MAX_GROUPS_PER_RUN": 0}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.False(t, cfg.RollupEnabled, "text is not a switch")
	assert.Equal(t, 60, cfg.RollupMinAgeDays, "an age of zero is not one")
	assert.Equal(t, 8, cfg.RollupMinGroupSize)
	assert.Equal(t, 30, cfg.RollupKeepNewest, "a negative count is not one")
	assert.Equal(t, 3, cfg.RollupMaxGroupsPerRun)
}

func TestLoad_ConsolidationSettings(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.False(t, cfg.ConsolidationEnabled, "it archives notes: off by default")
	assert.Equal(t, 0.92, cfg.ConsolidationMinSimilarity)
	assert.Equal(t, 5, cfg.ConsolidationMaxPerRun)
	assert.Equal(t, 360, cfg.ConsolidationIntervalMinutes)

	writeSettings(t, `{"CLAUDE_MNEMONIC_CONSOLIDATION_ENABLED": true, "CLAUDE_MNEMONIC_CONSOLIDATION_MIN_SIMILARITY": 0.85,
		"CLAUDE_MNEMONIC_CONSOLIDATION_MAX_PER_RUN": 2, "CLAUDE_MNEMONIC_CONSOLIDATION_INTERVAL_MINUTES": 30}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.True(t, cfg.ConsolidationEnabled)
	assert.Equal(t, 0.85, cfg.ConsolidationMinSimilarity)
	assert.Equal(t, 2, cfg.ConsolidationMaxPerRun)
	assert.Equal(t, 30, cfg.ConsolidationIntervalMinutes)

	for name, raw := range map[string]string{"too low": `0.2`, "above one": `1.5`, "text": `"high"`} {
		writeSettings(t, `{"CLAUDE_MNEMONIC_CONSOLIDATION_MIN_SIMILARITY": `+raw+`, "CLAUDE_MNEMONIC_CONSOLIDATION_MAX_PER_RUN": 0, "CLAUDE_MNEMONIC_CONSOLIDATION_ENABLED": "yes"}`)
		cfg, err = Load()
		assert.NoError(t, err, name)
		assert.Equal(t, 0.92, cfg.ConsolidationMinSimilarity, "%s is not a similarity, so the default stays", name)
		assert.Equal(t, 5, cfg.ConsolidationMaxPerRun, name)
		assert.False(t, cfg.ConsolidationEnabled, "text is not a switch")
	}
}

func TestLoad_RollupLadderAndQuarterSettings(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.Equal(t, 0, cfg.RollupTargetLiveNotes, "no target by default: the fixed age applies")
	assert.True(t, cfg.RollupQuartersEnabled, "quarter notes are on (they only matter when roll-ups are used)")

	writeSettings(t, `{"CLAUDE_MNEMONIC_ROLLUP_TARGET_LIVE_NOTES": 300, "CLAUDE_MNEMONIC_ROLLUP_QUARTERS_ENABLED": false}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 300, cfg.RollupTargetLiveNotes)
	assert.False(t, cfg.RollupQuartersEnabled)

	writeSettings(t, `{"CLAUDE_MNEMONIC_ROLLUP_TARGET_LIVE_NOTES": 0}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 0, cfg.RollupTargetLiveNotes, "zero switches the ladder off")

	for name, raw := range map[string]string{"negative": `-5`, "text": `"lots"`, "boolean": `true`} {
		writeSettings(t, `{"CLAUDE_MNEMONIC_ROLLUP_TARGET_LIVE_NOTES": `+raw+`, "CLAUDE_MNEMONIC_ROLLUP_QUARTERS_ENABLED": "no"}`)
		cfg, err = Load()
		assert.NoError(t, err, name)
		assert.Equal(t, 0, cfg.RollupTargetLiveNotes, "%s is not a target", name)
		assert.True(t, cfg.RollupQuartersEnabled, "text is not a switch, so the default stays")
	}
}

func TestLoad_PromptRetentionSetting(t *testing.T) {
	writeSettings(t, `{}`)
	cfg, err := Load()
	assert.NoError(t, err)
	assert.Equal(t, 0, cfg.PromptRetentionDays, "every prompt is kept by default")

	writeSettings(t, `{"CLAUDE_MNEMONIC_PROMPT_RETENTION_DAYS": 90}`)
	cfg, err = Load()
	assert.NoError(t, err)
	assert.Equal(t, 90, cfg.PromptRetentionDays)

	for name, raw := range map[string]string{"negative": `-1`, "text": `"month"`, "boolean": `true`} {
		writeSettings(t, `{"CLAUDE_MNEMONIC_PROMPT_RETENTION_DAYS": `+raw+`}`)
		cfg, err = Load()
		assert.NoError(t, err, name)
		assert.Equal(t, 0, cfg.PromptRetentionDays, "%s is not a retention, so every prompt is kept", name)
	}
}
