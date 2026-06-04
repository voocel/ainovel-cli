package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	SimulationProfileVersion        = "simulation_profile.v1"
	maxCompactSimulationSourceFiles = 20
	maxCompactSimulationItems       = 12
)

type SimulationProfile struct {
	Version       string                   `json:"version"`
	CreatedAt     string                   `json:"created_at,omitempty"`
	UpdatedAt     string                   `json:"updated_at,omitempty"`
	Corpus        SimulationCorpusManifest `json:"corpus"`
	SourceReports []SimulationSourceReport `json:"source_reports"`
	Synthesis     SimulationSynthesis      `json:"synthesis"`
}

type SimulationCorpusManifest struct {
	SourceDir string             `json:"source_dir,omitempty"`
	Sources   []SimulationSource `json:"sources"`
}

type SimulationSource struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Fingerprint  string `json:"fingerprint"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	ModTime      string `json:"mod_time,omitempty"`
	AnalyzedAt   string `json:"analyzed_at,omitempty"`
}

type SimulationSourceReport struct {
	RelativePath       string   `json:"relative_path,omitempty"`
	SHA256             string   `json:"sha256,omitempty"`
	Fingerprint        string   `json:"fingerprint,omitempty"`
	AnalyzedAt         string   `json:"analyzed_at,omitempty"`
	Title              string   `json:"title,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	StyleObservations  []string `json:"style_observations,omitempty"`
	CommonWords        []string `json:"common_words,omitempty"`
	PlotPatterns       []string `json:"plot_patterns,omitempty"`
	HookPatterns       []string `json:"hook_patterns,omitempty"`
	PacingNotes        []string `json:"pacing_notes,omitempty"`
	ReaderAppeal       []string `json:"reader_appeal,omitempty"`
	ReusableTechniques []string `json:"reusable_techniques,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

type SimulationSynthesis struct {
	Style            SimulationStyle            `json:"style,omitempty"`
	Lexicon          SimulationLexicon          `json:"lexicon,omitempty"`
	PlotDesign       SimulationPlotDesign       `json:"plot_design,omitempty"`
	HookDesign       SimulationHookDesign       `json:"hook_design,omitempty"`
	PacingDensity    SimulationPacingDensity    `json:"pacing_density,omitempty"`
	ReaderEngagement SimulationReaderEngagement `json:"reader_engagement,omitempty"`
	RoleGuidance     SimulationRoleGuidance     `json:"role_guidance,omitempty"`
}

type SimulationStyle struct {
	NarrativeVoice []string `json:"narrative_voice,omitempty"`
	SentenceRhythm []string `json:"sentence_rhythm,omitempty"`
	ProseTexture   []string `json:"prose_texture,omitempty"`
	Perspective    []string `json:"perspective,omitempty"`
	Mood           []string `json:"mood,omitempty"`
	DoNotCopy      []string `json:"do_not_copy,omitempty"`
}

type SimulationLexicon struct {
	CommonWords      []string `json:"common_words,omitempty"`
	EmotionWords     []string `json:"emotion_words,omitempty"`
	SceneWords       []string `json:"scene_words,omitempty"`
	TransitionWords  []string `json:"transition_words,omitempty"`
	SignaturePhrases []string `json:"signature_phrases,omitempty"`
}

type SimulationPlotDesign struct {
	OpeningPatterns      []string `json:"opening_patterns,omitempty"`
	EscalationPatterns   []string `json:"escalation_patterns,omitempty"`
	TurningPointPatterns []string `json:"turning_point_patterns,omitempty"`
	PayoffPatterns       []string `json:"payoff_patterns,omitempty"`
}

type SimulationHookDesign struct {
	HookTypes           []string `json:"hook_types,omitempty"`
	Placement           []string `json:"placement,omitempty"`
	CliffhangerPatterns []string `json:"cliffhanger_patterns,omitempty"`
	PayoffRules         []string `json:"payoff_rules,omitempty"`
}

type SimulationPacingDensity struct {
	SceneDensity        []string `json:"scene_density,omitempty"`
	InformationRelease  []string `json:"information_release,omitempty"`
	DialogueActionRatio []string `json:"dialogue_action_ratio,omitempty"`
	CompressionRules    []string `json:"compression_rules,omitempty"`
}

type SimulationReaderEngagement struct {
	Methods            []string `json:"methods,omitempty"`
	EmotionalDrivers   []string `json:"emotional_drivers,omitempty"`
	ProgressionRewards []string `json:"progression_rewards,omitempty"`
	AntiPatterns       []string `json:"anti_patterns,omitempty"`
}

type SimulationRoleGuidance struct {
	Coordinator []string `json:"coordinator,omitempty"`
	Architect   []string `json:"architect,omitempty"`
	Writer      []string `json:"writer,omitempty"`
	Editor      []string `json:"editor,omitempty"`
}

type SimulationCompactProfile struct {
	Version          string                     `json:"version"`
	UpdatedAt        string                     `json:"updated_at,omitempty"`
	SourceCount      int                        `json:"source_count"`
	SourceFiles      []string                   `json:"source_files,omitempty"`
	Style            SimulationStyle            `json:"style,omitempty"`
	Lexicon          SimulationLexicon          `json:"lexicon,omitempty"`
	PlotDesign       SimulationPlotDesign       `json:"plot_design,omitempty"`
	HookDesign       SimulationHookDesign       `json:"hook_design,omitempty"`
	PacingDensity    SimulationPacingDensity    `json:"pacing_density,omitempty"`
	ReaderEngagement SimulationReaderEngagement `json:"reader_engagement,omitempty"`
	RoleGuidance     SimulationRoleGuidance     `json:"role_guidance,omitempty"`
}

func SimulationSourceFingerprint(relativePath, sha256 string) string {
	return strings.TrimSpace(relativePath) + ":" + strings.TrimSpace(sha256)
}

func ValidateSimulationProfile(p *SimulationProfile) error {
	if p == nil {
		return fmt.Errorf("simulation profile is nil")
	}
	if p.Version != SimulationProfileVersion {
		return fmt.Errorf("unsupported simulation profile version %q", p.Version)
	}
	for i := range p.Corpus.Sources {
		source := &p.Corpus.Sources[i]
		if source.RelativePath == "" || source.SHA256 == "" {
			return fmt.Errorf("source[%d] requires relative_path and sha256", i)
		}
		if source.Fingerprint == "" {
			source.Fingerprint = SimulationSourceFingerprint(source.RelativePath, source.SHA256)
		}
	}
	for i := range p.SourceReports {
		report := &p.SourceReports[i]
		if report.Fingerprint == "" && report.RelativePath != "" && report.SHA256 != "" {
			report.Fingerprint = SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		}
	}
	return nil
}

func MarshalSimulationProfile(p SimulationProfile) ([]byte, error) {
	if p.Version == "" {
		p.Version = SimulationProfileVersion
	}
	return json.MarshalIndent(p, "", "  ")
}

func CompactSimulationProfile(p *SimulationProfile) *SimulationCompactProfile {
	if p == nil {
		return nil
	}
	limit := len(p.Corpus.Sources)
	if limit > maxCompactSimulationSourceFiles {
		limit = maxCompactSimulationSourceFiles
	}
	files := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		files = append(files, p.Corpus.Sources[i].RelativePath)
	}
	synthesis := compactSimulationSynthesis(p.Synthesis)
	return &SimulationCompactProfile{
		Version:          p.Version,
		UpdatedAt:        p.UpdatedAt,
		SourceCount:      len(p.Corpus.Sources),
		SourceFiles:      files,
		Style:            synthesis.Style,
		Lexicon:          synthesis.Lexicon,
		PlotDesign:       synthesis.PlotDesign,
		HookDesign:       synthesis.HookDesign,
		PacingDensity:    synthesis.PacingDensity,
		ReaderEngagement: synthesis.ReaderEngagement,
		RoleGuidance:     synthesis.RoleGuidance,
	}
}

func compactSimulationSynthesis(s SimulationSynthesis) SimulationSynthesis {
	return SimulationSynthesis{
		Style: SimulationStyle{
			NarrativeVoice: compactSimulationItems(s.Style.NarrativeVoice),
			SentenceRhythm: compactSimulationItems(s.Style.SentenceRhythm),
			ProseTexture:   compactSimulationItems(s.Style.ProseTexture),
			Perspective:    compactSimulationItems(s.Style.Perspective),
			Mood:           compactSimulationItems(s.Style.Mood),
			DoNotCopy:      compactSimulationItems(s.Style.DoNotCopy),
		},
		Lexicon: SimulationLexicon{
			CommonWords:      compactSimulationItems(s.Lexicon.CommonWords),
			EmotionWords:     compactSimulationItems(s.Lexicon.EmotionWords),
			SceneWords:       compactSimulationItems(s.Lexicon.SceneWords),
			TransitionWords:  compactSimulationItems(s.Lexicon.TransitionWords),
			SignaturePhrases: compactSimulationItems(s.Lexicon.SignaturePhrases),
		},
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns:      compactSimulationItems(s.PlotDesign.OpeningPatterns),
			EscalationPatterns:   compactSimulationItems(s.PlotDesign.EscalationPatterns),
			TurningPointPatterns: compactSimulationItems(s.PlotDesign.TurningPointPatterns),
			PayoffPatterns:       compactSimulationItems(s.PlotDesign.PayoffPatterns),
		},
		HookDesign: SimulationHookDesign{
			HookTypes:           compactSimulationItems(s.HookDesign.HookTypes),
			Placement:           compactSimulationItems(s.HookDesign.Placement),
			CliffhangerPatterns: compactSimulationItems(s.HookDesign.CliffhangerPatterns),
			PayoffRules:         compactSimulationItems(s.HookDesign.PayoffRules),
		},
		PacingDensity: SimulationPacingDensity{
			SceneDensity:        compactSimulationItems(s.PacingDensity.SceneDensity),
			InformationRelease:  compactSimulationItems(s.PacingDensity.InformationRelease),
			DialogueActionRatio: compactSimulationItems(s.PacingDensity.DialogueActionRatio),
			CompressionRules:    compactSimulationItems(s.PacingDensity.CompressionRules),
		},
		ReaderEngagement: SimulationReaderEngagement{
			Methods:            compactSimulationItems(s.ReaderEngagement.Methods),
			EmotionalDrivers:   compactSimulationItems(s.ReaderEngagement.EmotionalDrivers),
			ProgressionRewards: compactSimulationItems(s.ReaderEngagement.ProgressionRewards),
			AntiPatterns:       compactSimulationItems(s.ReaderEngagement.AntiPatterns),
		},
		RoleGuidance: SimulationRoleGuidance{
			Coordinator: compactSimulationItems(s.RoleGuidance.Coordinator),
			Architect:   compactSimulationItems(s.RoleGuidance.Architect),
			Writer:      compactSimulationItems(s.RoleGuidance.Writer),
			Editor:      compactSimulationItems(s.RoleGuidance.Editor),
		},
	}
}

func compactSimulationItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	limit := len(items)
	if limit > maxCompactSimulationItems {
		limit = maxCompactSimulationItems
	}
	out := make([]string, limit)
	copy(out, items[:limit])
	return out
}

func MergeSimulationSynthesis(a, b SimulationSynthesis) SimulationSynthesis {
	return SimulationSynthesis{
		Style: SimulationStyle{
			NarrativeVoice: mergeStringSets(a.Style.NarrativeVoice, b.Style.NarrativeVoice),
			SentenceRhythm: mergeStringSets(a.Style.SentenceRhythm, b.Style.SentenceRhythm),
			ProseTexture:   mergeStringSets(a.Style.ProseTexture, b.Style.ProseTexture),
			Perspective:    mergeStringSets(a.Style.Perspective, b.Style.Perspective),
			Mood:           mergeStringSets(a.Style.Mood, b.Style.Mood),
			DoNotCopy:      mergeStringSets(a.Style.DoNotCopy, b.Style.DoNotCopy),
		},
		Lexicon: SimulationLexicon{
			CommonWords:      mergeStringSets(a.Lexicon.CommonWords, b.Lexicon.CommonWords),
			EmotionWords:     mergeStringSets(a.Lexicon.EmotionWords, b.Lexicon.EmotionWords),
			SceneWords:       mergeStringSets(a.Lexicon.SceneWords, b.Lexicon.SceneWords),
			TransitionWords:  mergeStringSets(a.Lexicon.TransitionWords, b.Lexicon.TransitionWords),
			SignaturePhrases: mergeStringSets(a.Lexicon.SignaturePhrases, b.Lexicon.SignaturePhrases),
		},
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns:      mergeStringSets(a.PlotDesign.OpeningPatterns, b.PlotDesign.OpeningPatterns),
			EscalationPatterns:   mergeStringSets(a.PlotDesign.EscalationPatterns, b.PlotDesign.EscalationPatterns),
			TurningPointPatterns: mergeStringSets(a.PlotDesign.TurningPointPatterns, b.PlotDesign.TurningPointPatterns),
			PayoffPatterns:       mergeStringSets(a.PlotDesign.PayoffPatterns, b.PlotDesign.PayoffPatterns),
		},
		HookDesign: SimulationHookDesign{
			HookTypes:           mergeStringSets(a.HookDesign.HookTypes, b.HookDesign.HookTypes),
			Placement:           mergeStringSets(a.HookDesign.Placement, b.HookDesign.Placement),
			CliffhangerPatterns: mergeStringSets(a.HookDesign.CliffhangerPatterns, b.HookDesign.CliffhangerPatterns),
			PayoffRules:         mergeStringSets(a.HookDesign.PayoffRules, b.HookDesign.PayoffRules),
		},
		PacingDensity: SimulationPacingDensity{
			SceneDensity:        mergeStringSets(a.PacingDensity.SceneDensity, b.PacingDensity.SceneDensity),
			InformationRelease:  mergeStringSets(a.PacingDensity.InformationRelease, b.PacingDensity.InformationRelease),
			DialogueActionRatio: mergeStringSets(a.PacingDensity.DialogueActionRatio, b.PacingDensity.DialogueActionRatio),
			CompressionRules:    mergeStringSets(a.PacingDensity.CompressionRules, b.PacingDensity.CompressionRules),
		},
		ReaderEngagement: SimulationReaderEngagement{
			Methods:            mergeStringSets(a.ReaderEngagement.Methods, b.ReaderEngagement.Methods),
			EmotionalDrivers:   mergeStringSets(a.ReaderEngagement.EmotionalDrivers, b.ReaderEngagement.EmotionalDrivers),
			ProgressionRewards: mergeStringSets(a.ReaderEngagement.ProgressionRewards, b.ReaderEngagement.ProgressionRewards),
			AntiPatterns:       mergeStringSets(a.ReaderEngagement.AntiPatterns, b.ReaderEngagement.AntiPatterns),
		},
		RoleGuidance: SimulationRoleGuidance{
			Coordinator: mergeStringSets(a.RoleGuidance.Coordinator, b.RoleGuidance.Coordinator),
			Architect:   mergeStringSets(a.RoleGuidance.Architect, b.RoleGuidance.Architect),
			Writer:      mergeStringSets(a.RoleGuidance.Writer, b.RoleGuidance.Writer),
			Editor:      mergeStringSets(a.RoleGuidance.Editor, b.RoleGuidance.Editor),
		},
	}
}

func mergeStringSets(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			key := strings.ToLower(item)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}
