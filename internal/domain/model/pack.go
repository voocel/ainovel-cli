package model

import (
	"fmt"
	"strings"
)

type PackManifest struct {
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	Name           string            `json:"name"`
	PromptOverlays map[string]string `json:"prompt_overlays,omitempty"`
	Rules          []string          `json:"rules,omitempty"`
	References     []string          `json:"references,omitempty"`
	ReferenceData  map[string]string `json:"reference_data,omitempty"`
	Templates      []string          `json:"templates,omitempty"`
	TemplateData   map[string]string `json:"template_data,omitempty"`
	Evals          []string          `json:"evals,omitempty"`
	EvalData       map[string]string `json:"eval_data,omitempty"`
}

func (v PackManifest) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Version) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("pack id, version and name are required: %w", ErrInvalid)
	}
	for slot := range v.PromptOverlays {
		if strings.TrimSpace(slot) == "" {
			return fmt.Errorf("prompt overlay slot is required: %w", ErrInvalid)
		}
	}
	if err := validateDistinctStrings("pack rules", v.Rules); err != nil {
		return err
	}
	for _, assets := range []struct {
		name string
		keys []string
		data map[string]string
	}{
		{"references", v.References, v.ReferenceData},
		{"templates", v.Templates, v.TemplateData},
		{"evals", v.Evals, v.EvalData},
	} {
		if err := validateDistinctStrings("pack "+assets.name, assets.keys); err != nil {
			return err
		}
		if len(assets.keys) != len(assets.data) {
			return fmt.Errorf("pack %s index and data differ: %w", assets.name, ErrInvalid)
		}
		for _, key := range assets.keys {
			if _, ok := assets.data[key]; !ok {
				return fmt.Errorf("pack %s data %q is missing: %w", assets.name, key, ErrInvalid)
			}
		}
	}
	return nil
}
