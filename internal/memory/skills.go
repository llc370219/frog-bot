package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"frogbot/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

var skillSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// SkillMeta is the compact index entry injected into the frozen System Prompt prefix.
type SkillMeta struct {
	Name        string    `yaml:"name" json:"name"`
	Description string    `yaml:"description" json:"description"`
	UpdatedAt   time.Time `yaml:"updated_at" json:"updated_at"`
}

// Skill is the full skill definition persisted at <botDir>/skills/<name>/SKILL.md.
type Skill struct {
	SkillMeta
	StepsMarkdown string `json:"steps_markdown"`
}

// SkillStore manages evolvable SKILL.md files for a single Bot.
type SkillStore struct {
	Dir string
	mu  sync.RWMutex
}

func NewSkillStore(dir string) *SkillStore {
	return &SkillStore{Dir: dir}
}

func ValidSkillName(name string) error {
	if !skillSlugRe.MatchString(name) {
		return errors.New("技能名必须为小写字母、数字、下划线或连字符（如 daily_checkin）")
	}
	return nil
}

func (s *SkillStore) skillPath(name string) string {
	return filepath.Join(s.Dir, name, "SKILL.md")
}

// Save writes or updates skills/<name>/SKILL.md atomically with YAML frontmatter.
func (s *SkillStore) Save(name, description, stepsMarkdown string) (Skill, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	description = strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
	stepsMarkdown = strings.TrimSpace(stepsMarkdown)
	if err := ValidSkillName(name); err != nil {
		return Skill{}, err
	}
	if description == "" || stepsMarkdown == "" {
		return Skill{}, errors.New("description 和 steps_markdown 均不能为空")
	}
	sk := Skill{
		SkillMeta: SkillMeta{
			Name:        name,
			Description: truncateRunes(description, 160),
			UpdatedAt:   time.Now().UTC(),
		},
		StepsMarkdown: stepsMarkdown,
	}
	fm, err := yaml.Marshal(&sk.SkillMeta)
	if err != nil {
		return Skill{}, err
	}
	doc := fmt.Sprintf("---\n%s\n---\n\n%s\n", strings.TrimSpace(string(fm)), sk.StepsMarkdown)

	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.skillPath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return Skill{}, err
	}
	if err := atomicfile.Write(p, []byte(doc), 0o600); err != nil {
		return Skill{}, err
	}
	return sk, nil
}

// Load expands a saved SKILL.md on demand.
func (s *SkillStore) Load(name string) (Skill, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := ValidSkillName(name); err != nil {
		return Skill{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, err := os.ReadFile(s.skillPath(name))
	if err != nil {
		return Skill{}, err
	}
	return parseSkillDoc(name, string(raw)), nil
}

// List returns metadata for all installed skills sorted by name.
func (s *SkillStore) List() []SkillMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []SkillMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(s.skillPath(e.Name()))
		if err != nil {
			continue
		}
		sk := parseSkillDoc(e.Name(), string(raw))
		out = append(out, sk.SkillMeta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StaticIndex renders a minimal 1-line-per-skill index for the frozen System Prompt.
func (s *SkillStore) StaticIndex() string {
	list := s.List()
	if len(list) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, sk := range list {
		fmt.Fprintf(&sb, "- %s: %s\n", sk.Name, sk.Description)
	}
	return strings.TrimSpace(sb.String())
}

func parseSkillDoc(fallbackName, raw string) Skill {
	sk := Skill{SkillMeta: SkillMeta{Name: fallbackName}}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "---\n") {
		rest := trimmed[4:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			fm := rest[:idx]
			body := strings.TrimSpace(rest[idx+4:])
			_ = yaml.Unmarshal([]byte(fm), &sk.SkillMeta)
			if sk.Name == "" {
				sk.Name = fallbackName
			}
			sk.StepsMarkdown = body
			return sk
		}
	}
	sk.StepsMarkdown = trimmed
	return sk
}
