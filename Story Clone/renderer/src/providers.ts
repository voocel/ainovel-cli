export type ProviderPreset = {
  name: string;
  label: string;
  type: string;
  base_url: string;
  models: string[];
};

export const PROVIDERS: ProviderPreset[] = [
  {
    name: "9router",
    label: "9Router Cục bộ",
    type: "openai",
    base_url: "http://localhost:20128/v1",
    models: [
      "ag/gemini-3.5-flash-low",
      "ag/gemini-3.1-pro-low",
      "ag/gemini-pro-agent",
      "mmf/mimo-auto",
      "openrouter/google/gemma-4-26b-a4b-it:free",
      "oc/deepseek-v4-flash-free",
      "oc/mimo-v2.5-free"
    ]
  },
  {
    name: "gemini",
    label: "Google Gemini",
    type: "gemini",
    base_url: "",
    models: ["gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro", "gemini-3-flash-preview", "gemini-3.1-flash-lite", "gemini-3.1-pro-preview", "gemini-3.5-flash"]
  }
];

export function mergeProviderModels(providerName: string, catalog?: Record<string, string[]>, saved?: string[]) {
  const preset = PROVIDERS.find(p => p.name === providerName);

  // For providers with a fixed preset list (e.g. 9router), only show preset models
  // so that stale catalog/saved entries from the backend don't pollute the dropdown.
  if (preset && preset.models.length > 0) {
    return [...preset.models];
  }

  const seen = new Set<string>();
  const out: string[] = [];
  const add = (model?: string) => {
    if (!model || seen.has(model)) return;
    seen.add(model);
    out.push(model);
  };
  for (const model of catalog?.[providerName] ?? []) add(model);
  for (const model of preset?.models ?? []) add(model);
  for (const model of saved ?? []) add(model);
  return out;
}
