from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

import httpx

from app.repositories.repository import repo

@dataclass
class LLMResponse:
    text: str
    provider: str = "mock"
    model: str = "mock"
    input_tokens: int = 0
    output_tokens: int = 0
    cost_usd: float = 0.0

THINKING_MAP = {
    "off": {"reasoning_effort": "none"},
    "minimal": {"reasoning_effort": "low"},
    "low": {"reasoning_effort": "low"},
    "medium": {"reasoning_effort": "medium"},
    "high": {"reasoning_effort": "high"},
    "xhigh": {"reasoning_effort": "high"},
    "max": {"reasoning_effort": "high"},
}

class LLMClient:
    async def generate(self, role: str, messages: list[dict[str, str]], project_id: str | None = None, agent_name: str | None = None) -> LLMResponse:
        # Enforce Vietnamese language instruction in the system prompt
        vietnamese_instruction = (
            "\n\nLƯU Ý QUAN TRỌNG VỀ NGÔN NGỮ: Mọi nội dung phản hồi, văn bản tạo ra, dàn ý (outline), "
            "tóm tắt (summary), nhân vật, đánh giá, các trường văn bản trong JSON, và đặc biệt là "
            "nội dung chương truyện BẮT BUỘC PHẢI VIẾT BẰNG TIẾNG VIỆT (Vietnamese). "
            "Không sử dụng tiếng Trung hay bất kỳ ngôn ngữ nào khác."
        )
        
        has_system = False
        new_messages = []
        for msg in messages:
            if msg.get("role") == "system":
                new_messages.append({"role": "system", "content": msg.get("content", "") + vietnamese_instruction})
                has_system = True
            else:
                new_messages.append(msg)
        if not has_system:
            new_messages.insert(0, {"role": "system", "content": vietnamese_instruction.strip()})
        
        messages = new_messages

        role_models = {r["role"]: r for r in repo.role_models()}
        selected = role_models.get(role) or role_models.get("default")
        candidates: list[tuple[dict[str, Any], str]] = []
        if selected:
            candidates.append((selected, selected["provider"]))
            for fb in selected.get("fallbacks") or []:
                if fb.get("provider"):
                    candidates.append(({**selected, **fb}, fb["provider"]))
        if not candidates:
            resp = self._mock(role, messages)
            self._record(project_id, agent_name or role, resp, messages)
            return resp

        last_err: Exception | None = None
        for sel, provider_name in candidates:
            provider = repo.provider_secret(provider_name)
            if not provider:
                continue
            if not provider.get("api_key") and provider_name not in {"ollama"}:
                continue
            ptype = provider.get("type") or provider_name
            try:
                if ptype in {"openai", "openrouter", "deepseek", "qwen", "glm", "grok", "mimo", "ollama"}:
                    resp = await self._openai_compatible(provider, sel["model"], messages, provider_name, sel.get("thinking"))
                elif ptype == "anthropic":
                    resp = await self._anthropic(provider, sel["model"], messages, provider_name)
                elif ptype == "gemini":
                    resp = await self._gemini(provider, sel["model"], messages, provider_name)
                else:
                    resp = await self._openai_compatible(provider, sel["model"], messages, provider_name, sel.get("thinking"))
                self._record(project_id, agent_name or role, resp, messages)
                return resp
            except Exception as exc:
                last_err = exc
                continue

        resp = self._mock(role, messages, selected["provider"] if selected else "mock", selected["model"] if selected else "mock")
        if last_err and project_id:
            repo.event(project_id, "SYSTEM", f"LLM fallback mock: {last_err}", "warn")
        self._record(project_id, agent_name or role, resp, messages)
        return resp

    def _record(self, project_id: str | None, agent_name: str, resp: LLMResponse, messages: list[dict[str, str]]) -> None:
        repo.record_usage(project_id, agent_name, resp.provider, resp.model, resp.input_tokens, resp.output_tokens, resp.cost_usd)
        if project_id:
            repo.record_agent_message(project_id, agent_name, "assistant", resp.provider, resp.model, messages[-3:])

    async def _openai_compatible(self, provider: dict[str, Any], model: str, messages: list[dict[str, str]], provider_name: str, thinking: str | None = None) -> LLMResponse:
        base_url = (provider.get("base_url") or "https://api.openai.com/v1").rstrip("/")
        headers = {"Content-Type": "application/json"}
        if provider.get("api_key"):
            headers["Authorization"] = f"Bearer {provider['api_key']}"
        body: dict[str, Any] = {"model": model, "messages": messages, "stream": False}
        body.update(provider.get("extra_body") or {})
        if thinking and thinking in THINKING_MAP:
            body.update(THINKING_MAP[thinking])
        # 9router (localhost) cần thêm thời gian xử lý — tăng lên 4 phút
        request_timeout = 240 if provider_name == "9router" else 120
        async with httpx.AsyncClient(timeout=httpx.Timeout(request_timeout, connect=10.0)) as client:
            res = await client.post(f"{base_url}/chat/completions", headers=headers, json=body)
            res.raise_for_status()
            data = res.json()
        text = data.get("choices", [{}])[0].get("message", {}).get("content", "")
        usage = data.get("usage") or {}
        return LLMResponse(text=text, provider=provider_name, model=model, input_tokens=usage.get("prompt_tokens", 0), output_tokens=usage.get("completion_tokens", 0))

    async def _anthropic(self, provider: dict[str, Any], model: str, messages: list[dict[str, str]], provider_name: str) -> LLMResponse:
        base_url = (provider.get("base_url") or "https://api.anthropic.com").rstrip("/")
        headers = {"Content-Type": "application/json", "anthropic-version": "2023-06-01", "x-api-key": provider.get("api_key", "")}
        system = "\n".join(m["content"] for m in messages if m.get("role") == "system")
        user_msgs = [{"role": "user" if m["role"] == "user" else "assistant", "content": m["content"]} for m in messages if m.get("role") != "system"]
        body = {"model": model, "max_tokens": 8192, "messages": user_msgs}
        if system:
            body["system"] = system
        async with httpx.AsyncClient(timeout=120) as client:
            res = await client.post(f"{base_url}/v1/messages", headers=headers, json=body)
            res.raise_for_status()
            data = res.json()
        text = "".join(b.get("text", "") for b in data.get("content", []) if b.get("type") == "text")
        usage = data.get("usage") or {}
        return LLMResponse(text=text, provider=provider_name, model=model, input_tokens=usage.get("input_tokens", 0), output_tokens=usage.get("output_tokens", 0))

    async def _gemini(self, provider: dict[str, Any], model: str, messages: list[dict[str, str]], provider_name: str) -> LLMResponse:
        api_key = provider.get("api_key", "")
        base = provider.get("base_url") or f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent"
        if "generateContent" not in base:
            base = f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent"
        parts = [{"text": m["content"]} for m in messages]
        body = {"contents": [{"role": "user", "parts": parts}]}
        async with httpx.AsyncClient(timeout=120) as client:
            res = await client.post(f"{base}?key={api_key}", json=body)
            res.raise_for_status()
            data = res.json()
        text = data.get("candidates", [{}])[0].get("content", {}).get("parts", [{}])[0].get("text", "")
        usage = data.get("usageMetadata") or {}
        return LLMResponse(text=text, provider=provider_name, model=model, input_tokens=usage.get("promptTokenCount", 0), output_tokens=usage.get("candidatesTokenCount", 0))

    def _mock(self, role: str, messages: list[dict[str, str]], provider: str = "mock", model: str = "mock") -> LLMResponse:
        user = "\n".join(m.get("content", "") for m in messages if m.get("role") == "user")[-2000:]
        if role.startswith("architect") or role == "architect":
            text = json.dumps({
                "premise": f"# Tác phẩm mô phỏng\n\nÝ tưởng trung tâm: {user or 'một câu chuyện dài tập'}",
                "outline": [{"chapter": i, "title": f"Chương {i}", "goal": "Đẩy xung đột và phát triển nhân vật"} for i in range(1, 6)],
                "characters": [{"name": "Nhân vật chính", "role": "protagonist", "description": "Người mang mục tiêu trung tâm của câu chuyện."}],
                "world_rules": [{"name": "Luật thế giới", "description": "Thiết lập được giữ nhất quán xuyên suốt."}],
                "compass": {"ending_direction": "Khép lại tuyến chính với biến đổi rõ ràng của nhân vật."},
            }, ensure_ascii=False)
        elif role == "editor":
            text = json.dumps({"verdict": "accept", "score": 82, "dimensions": [{"name": "coherence", "score": 82}], "notes": "Bản mock chấp nhận chương."}, ensure_ascii=False)
        elif role == "artist":
            import re
            user = "\n".join(m.get("content", "") for m in messages if m.get("role") == "user")
            system = "\n".join(m.get("content", "") for m in messages if m.get("role") == "system")
            is_video_batch = '"video_prompts"' in system and '"image_prompts"' not in system
            wc_match = re.search(r'"word_count"\s*:\s*(\d+)', user)
            required_match = re.search(r'"required_count"\s*:\s*(\d+)', user)
            target_match = re.search(r'"target_image_prompt_count"\s*:\s*(\d+)', user)
            word_count = int(wc_match.group(1)) if wc_match else 300
            if required_match:
                target = int(required_match.group(1))
            elif target_match:
                target = int(target_match.group(1))
            else:
                target = max(1, round(word_count * 60 / 150 / 20))
            ink_style = (
                "traditional Vietnamese historical art, ink and wash illustration, woodblock print aesthetic, "
                "vintage parchment texture, sepia tones, earthy palette, cross-hatching shadows, epic historical narrative"
            )
            segments_match = re.findall(r'"excerpt"\s*:\s*"([^"]*)"', user)
            image_prompts = []
            video_prompts = []
            for i in range(target):
                seg = i + 1
                excerpt = segments_match[i] if i < len(segments_match) else f"đoạn thuyết minh {seg}"
                image_prompts.append({
                    "segment_index": seg,
                    "scene": f"Đoạn {seg}/{target}",
                    "moment": excerpt[:120] or f"Khoảnh khắc thứ {seg}",
                    "source_excerpt": excerpt,
                    "prompt": excerpt[:200] or f"đoạn thuyết minh {seg}",
                    "negative_prompt": "photorealistic modern, 3D render, anime, neon, chữ, logo, hiện đại hóa sai bối cảnh",
                    "style_notes": f"{ink_style}. ink and wash, sepia parchment, cross-hatching, Đông Hồ woodblock feel",
                    "characters": ["Nhân vật chính"],
                })
                video_prompts.append({
                    "segment_index": seg,
                    "scene": f"Video đoạn {seg}/{target}",
                    "duration": "20s",
                    "prompt": excerpt[:200] or f"Cảnh video minh họa đoạn {seg}",
                    "negative_prompt": "rung lắc lỗi, biến dạng khuôn mặt, chữ, logo, sai bối cảnh",
                    "camera": "slow pan, medium shot",
                    "motion": "chuyển động nhẹ theo nhịp kể",
                    "sound_mood": "thuyết minh lịch sử, nhịp điềm tĩnh",
                })
            if is_video_batch:
                text = json.dumps({"video_prompts": video_prompts}, ensure_ascii=False)
            elif '"task": "video_only"' in user:
                text = json.dumps({"video_prompts": video_prompts}, ensure_ascii=False)
            else:
                text = json.dumps({"image_prompts": image_prompts}, ensure_ascii=False)
        elif role == "coordinator":
            text = json.dumps({"action": "continue", "agent": "writer", "task": "Tiếp tục viết chương tiếp theo"}, ensure_ascii=False)
        else:
            text = "# Chương mô phỏng\n\nĐây là nội dung mock khi chưa cấu hình API key. Cấu hình provider trong tab Mô hình để dùng LLM thật.\n\nNhân vật chính đối mặt lựa chọn quan trọng, mở xung đột mới và giữ móc cho chương sau."
        return LLMResponse(text=text, provider=provider, model=model, input_tokens=len(user.split()), output_tokens=len(text.split()))

llm_client = LLMClient()

