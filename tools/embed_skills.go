package tools

import (
	"embed"
	"io/fs"
	"path"
)

// EmbeddedSkills contains skill templates built into the binary.
//
//go:embed embed_skills/*
var EmbeddedSkills embed.FS

// ListEmbeddedSkills returns names of all embedded skills (directory names under embed_skills/).
func ListEmbeddedSkills() []string {
	entries, err := fs.ReadDir(EmbeddedSkills, "embed_skills")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// ReadEmbeddedSkillFile reads a file from an embedded skill directory.
// skillName is the directory name (e.g., "agent-creator").
// file is the relative path within the skill directory (e.g., "SKILL.md").
// Returns nil if the file doesn't exist.
func ReadEmbeddedSkillFile(skillName, file string) ([]byte, error) {
	path := path.Join("embed_skills", skillName, file)
	return EmbeddedSkills.ReadFile(path)
}

// ListEmbeddedSkillFiles returns all file paths (relative to skill root) in an embedded skill.
func ListEmbeddedSkillFiles(skillName string) ([]string, error) {
	dir := path.Join("embed_skills", skillName)
	entries, err := fs.ReadDir(EmbeddedSkills, dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// HasEmbeddedSkill checks if a skill with the given name exists in the embedded FS.
func HasEmbeddedSkill(name string) bool {
	for _, n := range ListEmbeddedSkills() {
		if n == name {
			return true
		}
	}
	return false
}
