package version

// Set via ldflags at build time:
//   -ldflags "-X github.com/sourcebridge/sourcebridge/internal/version.Version=1.0.0
//             -X github.com/sourcebridge/sourcebridge/internal/version.Commit=abc123
//             -X github.com/sourcebridge/sourcebridge/internal/version.BuildDate=2026-03-17"
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
