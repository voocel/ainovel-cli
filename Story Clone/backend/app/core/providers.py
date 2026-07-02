from __future__ import annotations

from typing import Any

# Danh mục nhà cung cấp và mô hình — đồng bộ với ainovel-cli gốc (bootstrap/setup + models registry).
PROVIDER_PRESETS: list[dict[str, Any]] = [
    {
        "name": "9router",
        "label": "9Router Cục bộ",
        "type": "openai",
        "base_url": "http://localhost:20128/v1",
        "models": [
            "ag/gemini-3.5-flash-low",
            "ag/gemini-3.1-pro-low",
            "ag/gemini-pro-agent",
            "mmf/mimo-auto",
            "openrouter/google/gemma-4-26b-a4b-it:free",
            "oc/deepseek-v4-flash-free",
            "oc/mimo-v2.5-free",
        ],
    },
    {
        "name": "gemini",
        "label": "Google Gemini",
        "type": "gemini",
        "base_url": "",
        "models": [
            "gemini-2.5-flash",
            "gemini-2.5-flash-lite",
            "gemini-2.5-pro",
            "gemini-3-flash-preview",
            "gemini-3.1-flash-lite",
            "gemini-3.1-pro-preview",
            "gemini-3.5-flash",
        ],
    },
]


def list_provider_presets() -> list[dict[str, Any]]:
    return PROVIDER_PRESETS


def models_for_provider(provider_name: str, saved_models: list[str] | None = None) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for preset in PROVIDER_PRESETS:
        if preset["name"] == provider_name:
            for model in preset.get("models") or []:
                if model not in seen:
                    seen.add(model)
                    out.append(model)
    for model in saved_models or []:
        if model and model not in seen:
            seen.add(model)
            out.append(model)
    return out
