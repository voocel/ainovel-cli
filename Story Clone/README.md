# Story Clone

Ứng dụng desktop độc lập được triển khai lại từ `ainovel-cli` theo kiến trúc:

- Electron main process
- React + TypeScript renderer
- FastAPI backend local
- SQLite database
- Assets/prompt/rules/styles copy từ dự án gốc

## Chức năng đã triển khai

- Quản lý nhiều dự án tiểu thuyết.
- Start/resume/abort/continue/steer runtime.
- Pipeline agent: Architect -> Writer -> Editor.
- Foundation: premise, outline, characters, world rules, compass.
- Chapter pipeline: plan, draft, review, commit.
- Checkpoint theo step và digest idempotent.
- Event stream realtime qua WebSocket.
- SQLite store cho projects, progress, chapters, artifacts, checkpoints, reviews, messages, events, providers, role models, usage.
- Provider/model settings, OpenAI-compatible client, mock LLM fallback khi chưa cấu hình API key.
- Import tiểu thuyết TXT/MD.
- Export TXT/EPUB.
- Simulation profile từ corpus folder hoặc profile JSON.
- Diagnostics report.
- React UI cho dashboard, chapters, outline, reviews, diagnostics, tools, settings.

## Cài đặt backend

```bash
cd "Story Clone/backend"
python -m venv .venv
.venv\Scripts\activate
pip install -r requirements.txt
python -m uvicorn app.main:app --host 127.0.0.1 --port 8766 --reload
```

## Cài đặt desktop/frontend

```bash
cd "Story Clone"
npm install
npm run dev
```

Nếu chỉ muốn chạy backend để thử API:

```bash
cd "Story Clone/backend"
python -m uvicorn app.main:app --host 127.0.0.1 --port 8766
```

Mở tài liệu API:

```text
http://127.0.0.1:8766/docs
```

## Cấu hình AI

Trong tab `Model`, thêm provider. Ví dụ OpenRouter:

- name: `openrouter`
- type: `openai`
- base_url: `https://openrouter.ai/api/v1`
- api_key: API key của bạn
- models: `google/gemini-2.5-flash`

Nếu chưa cấu hình provider/API key, backend dùng mock LLM để pipeline vẫn chạy được end-to-end.

## Ghi chú triển khai

Bản này là port desktop có thể chạy độc lập, giữ đầy đủ bề mặt chức năng trong `PROJECT_ANALYSIS_DESKTOP_REIMPLEMENTATION.md`. Một số phần AI chuyên sâu của Go gốc được triển khai theo phiên bản Python tối giản nhưng cùng contract: tool -> artifact -> progress -> checkpoint -> event.

