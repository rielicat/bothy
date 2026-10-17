package picture

import _ "embed"

// default.png and default.jpg are rendered from the brand mark by
// TestDefaultPicture (go test ./internal/picture -run TestDefaultPicture -update).
var (
	//go:embed default.png
	defaultPNG []byte
	//go:embed default.jpg
	defaultJPEG []byte
)
