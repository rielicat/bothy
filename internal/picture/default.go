package picture

import _ "embed"

// default.png and default.jpg are rendered from web/public/icon-maskable.svg
// by make icons; TestDefaultPicture checks them against it.
var (
	//go:embed default.png
	defaultPNG []byte
	//go:embed default.jpg
	defaultJPEG []byte
)
