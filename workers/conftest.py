import sys
from pathlib import Path

# Add gen/python to sys.path so protobuf-generated stubs (common.v1, reasoning.v1, etc.) are importable
_gen_python = str(Path(__file__).resolve().parent.parent / "gen" / "python")
if _gen_python not in sys.path:
    sys.path.insert(0, _gen_python)
