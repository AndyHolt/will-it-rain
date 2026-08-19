"""Generate the golden fixtures the Go backend's parity tests assert against.

Protects against drift between model training in Python and inference serving in
Go backend. Ensures that predictions made using go backend match probability and
calibration exactly to python version, when using identical features.

"""

from pathlib import Path

# Fixtures live beside the Go code that consumes them; `testdata` is the name
# the Go toolchain already excludes from builds.
FIXTURE_DIR = Path("backend-go/testdata")
