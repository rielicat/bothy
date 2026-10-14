package config

// KnowledgeCfg configures the v0.2 repository index and project notes.
type KnowledgeCfg struct {
	Enabled bool `yaml:"enabled"`
	// AutoNotes drafts a repo's NOTES.md after its first index (fast slot,
	// metered against the principal's user).
	AutoNotes     bool `yaml:"auto_notes"`
	MaxFileKB     int  `yaml:"max_file_kb"`     // larger files are skipped
	MaxFiles      int  `yaml:"max_files"`       // per repo
	MaxRepoMB     int  `yaml:"max_repo_mb"`     // indexed bytes per repo
	NotesMaxBytes int  `yaml:"notes_max_bytes"` // per NOTES.md
	ContextBytes  int  `yaml:"context_bytes"`   // notes summaries per turn-context; 0 = none
}

func defaultKnowledge() KnowledgeCfg {
	return KnowledgeCfg{Enabled: true, AutoNotes: true, MaxFileKB: 256, MaxFiles: 20000, MaxRepoMB: 64,
		NotesMaxBytes: 16384, ContextBytes: 2048}
}

func validateKnowledge(c *Config, v *vctx) {
	k := c.Knowledge
	v.intRange("knowledge.max_file_kb", k.MaxFileKB, 1, 4096)
	v.intRange("knowledge.max_files", k.MaxFiles, 1, 1_000_000)
	v.intRange("knowledge.max_repo_mb", k.MaxRepoMB, 1, 4096)
	v.intRange("knowledge.notes_max_bytes", k.NotesMaxBytes, 1024, 1<<20)
	v.intRange("knowledge.context_bytes", k.ContextBytes, 0, 65536)
}
