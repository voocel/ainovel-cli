# -*- mode: python ; coding: utf-8 -*-
# run_backend.spec — PyInstaller spec cho Story Clone backend
# Dùng onedir mode (nhanh hơn onefile, không cần giải nén mỗi lần chạy)

import os
from PyInstaller.utils.hooks import collect_data_files, collect_submodules

# Thu thập tất cả hidden imports cần thiết
hidden = []
hidden += collect_submodules('uvicorn')
hidden += collect_submodules('fastapi')
hidden += collect_submodules('starlette')
hidden += collect_submodules('pydantic')
hidden += collect_submodules('pydantic_core')
hidden += collect_submodules('httpx')
hidden += collect_submodules('httpcore')
hidden += collect_submodules('anyio')
hidden += collect_submodules('anyio._backends')
hidden += collect_submodules('asyncio')
hidden += collect_submodules('multipart')
hidden += collect_submodules('email_validator')
hidden += collect_submodules('docx')
hidden += collect_submodules('lxml')
hidden += collect_submodules('reportlab')
hidden += collect_submodules('app')
hidden += [
    'uvicorn.logging',
    'uvicorn.loops',
    'uvicorn.loops.auto',
    'uvicorn.loops.asyncio',
    'uvicorn.protocols',
    'uvicorn.protocols.http',
    'uvicorn.protocols.http.auto',
    'uvicorn.protocols.websockets',
    'uvicorn.protocols.websockets.auto',
    'uvicorn.lifespan',
    'uvicorn.lifespan.on',
    'uvicorn.middleware',
    'uvicorn.middleware.proxy_headers',
    'fastapi.middleware',
    'fastapi.middleware.cors',
    'starlette.middleware',
    'starlette.middleware.cors',
    'starlette.routing',
    'starlette.responses',
    'starlette.staticfiles',
    'sqlite3',
    'aiosqlite',
    '_sqlite3',
    'websockets',
    'websockets.legacy',
    'websockets.legacy.server',
    'h11',
    'h2',
    'wsproto',
    'click',
    'dotenv',
    'typing_extensions',
    'sniffio',
]

# Thu thập data files (assets, prompts, references...)
datas = []
datas += collect_data_files('uvicorn')
datas += collect_data_files('fastapi')
datas += collect_data_files('starlette')
datas += collect_data_files('docx')
datas += collect_data_files('reportlab')

# Thêm toàn bộ thư mục app/assets vào bundle
datas += [('app/assets', 'app/assets')]

a = Analysis(
    ['run_backend.py'],
    pathex=['.'],
    binaries=[],
    datas=datas,
    hiddenimports=hidden,
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[
        'tkinter', 'matplotlib', 'numpy', 'pandas',
        'cv2', 'scipy', 'sklearn',
        'PyQt5', 'wx', 'gi',
    ],
    noarchive=False,
    optimize=1,
)

pyz = PYZ(a.pure)

# onedir: nhanh hơn onefile, không cần giải nén mỗi lần
exe = EXE(
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name='run_backend',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    upx_exclude=[],
    console=False,          # windowsHide — không hiện console khi chạy
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)

coll = COLLECT(
    exe,
    a.binaries,
    a.datas,
    strip=False,
    upx=True,
    upx_exclude=[],
    name='run_backend',     # → thư mục dist/run_backend/
)
