from __future__ import annotations

import asyncio
from collections import defaultdict
from typing import Any

class EventBus:
    def __init__(self) -> None:
        self._queues: dict[str, set[asyncio.Queue]] = defaultdict(set)

    async def publish(self, project_id: str, event: dict[str, Any]) -> None:
        for q in list(self._queues.get(project_id, set())):
            await q.put(event)

    async def subscribe(self, project_id: str):
        q: asyncio.Queue = asyncio.Queue(maxsize=200)
        self._queues[project_id].add(q)
        try:
            while True:
                try:
                    yield await q.get()
                except asyncio.CancelledError:
                    break
        finally:
            self._queues[project_id].discard(q)

bus = EventBus()
