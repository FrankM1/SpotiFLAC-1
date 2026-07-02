package backend

import "fmt"

// AllowCommunityServers controls whether the app is permitted to fall back to
// the hosted "community" relay servers (*-foss.spotbye.qzz.io). Those servers
// are effectively dead (they return HTTP 503 cooldowns), so the app is
// configured to use only locally-configured / self-hosted servers.
//
// Set to true only if the community relays come back and you want the old
// behavior. When false, any provider without a local server configured fails
// with a clear error instead of hanging on a dead endpoint.
var AllowCommunityServers = false

// errNoLocalServer builds the error shown when a provider has no local server
// configured and community fallback is disabled.
func errNoLocalServer(provider, howToConfigure string) error {
	return fmt.Errorf("no local %s server configured and community servers are disabled — %s", provider, howToConfigure)
}
