// Package version carries the build's release version.
//
// It exists so the version a running vibeD reports (e.g. to MCP clients in the
// initialize handshake) is the version that was actually shipped. It used to be
// a literal in the MCP server, which meant it still claimed "0.1.0" five
// releases later.
package version

// Version is the release this binary was built from. The release image build
// injects it at link time:
//
//	go build -ldflags "-X github.com/vibed-project/vibeD/internal/version.Version=0.6.0"
//
// Local and development builds leave it as "dev" — deliberately not a real
// version number, so an unreleased binary can never masquerade as a release.
var Version = "dev"
