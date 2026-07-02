"""
run_backend.py — Entry point cho PyInstaller bundle
Xử lý đường dẫn đúng khi chạy dưới dạng frozen executable.
"""
import os
import sys


def _prepare_runtime() -> None:
    if getattr(sys, "frozen", False):
        base = os.path.dirname(sys.executable)
        sys.path.insert(0, base)
        os.chdir(base)

    # PyInstaller console=False → stdout/stderr có thể là None; uvicorn logging cần stream hợp lệ.
    if sys.stdout is None or sys.stderr is None:
        sink = open(os.devnull, "w", encoding="utf-8", errors="replace")
        if sys.stdout is None:
            sys.stdout = sink
        if sys.stderr is None:
            sys.stderr = sink if sys.stdout is not sink else open(os.devnull, "w", encoding="utf-8", errors="replace")


_prepare_runtime()

import uvicorn
from app.main import app

if __name__ == "__main__":
    uvicorn.run(
        app,
        host="127.0.0.1",
        port=8766,
        log_level="warning",
        use_colors=False,
    )
