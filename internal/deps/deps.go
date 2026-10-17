//go:build deps

// Package deps blank-imports every go.mod dependency (SPEC §11.1) so that
// `go mod tidy` keeps them before the modules that use them land. It is never
// built without the deps tag.
package deps

import (
	_ "github.com/adhocore/gronx"
	_ "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	_ "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	_ "github.com/aws/aws-sdk-go-v2/config"
	_ "github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	_ "github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	_ "github.com/disgoorg/disgo"
	_ "github.com/elastic/go-seccomp-bpf"
	_ "github.com/google/jsonschema-go/jsonschema"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/slack-go/slack"
	_ "github.com/slack-go/slack/socketmode"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "golang.org/x/net/bpf"
	_ "golang.org/x/net/html"
	_ "golang.org/x/net/idna"
	_ "modernc.org/libc"
	_ "modernc.org/sqlite"
)
