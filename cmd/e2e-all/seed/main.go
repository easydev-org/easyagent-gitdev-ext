// Write the configuration an extension needs into the KV bucket it reads at startup.
//
// WHY THIS AND NOT A CONFIG MESSAGE: the extension subscribes to `easybus.*.config.<id>` and ALSO
// watches the `cfg` KV bucket, which it recovers from on boot. A message needs something listening at
// the moment it is sent; a KV write does not — so this can run before the extension starts, and a
// restarted extension still finds its configuration.
//
// The key layout is the SDK's: `t.<tenant>.<extId>.<name>` for a global value, with one more segment
// for a session-scoped one.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/easydev-org/easybus-sdk/transport/nats"
)

func main() {
	var (
		natsURL = flag.String("nats", envOr("NATS_URL", "nats://127.0.0.1:4222"), "NATS url")
		tenant  = flag.String("tenant", envOr("E2E_TENANT", "alice"), "tenant")
		extID   = flag.String("ext", envOr("E2E_EXT", "gitdev"), "extension id")
	)
	flag.Parse()

	b, err := nats.Connect(*natsURL)
	if err != nil {
		fatalf("connect: %v", err)
	}
	defer func() { _ = b.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// THE MIRROR'S SERVERS. `host` is what the extension matches a remote against, `kind` decides
	// which API it speaks, and the token is what lets it write.
	servers := os.Getenv("E2E_SERVERS")
	if servers == "" {
		fatalf("E2E_SERVERS is required (a JSON array of {host,kind,api_url,token})")
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		fatalf("GH_TOKEN is required")
	}

	values := map[string]string{
		"servers": servers,
		"token":   token,
	}
	// watch-repos is what makes the release watcher subscribe instead of polling nothing.
	if repo := os.Getenv("GH_E2E_REPO"); repo != "" {
		values["watch-repos"] = repo
	}

	for name, value := range values {
		key := fmt.Sprintf("t.%s.%s.%s", *tenant, *extID, name)
		// NO TTL (0): this is configuration, not a lease. A value that expired mid-suite would turn a
		// passing test into a puzzling one.
		if err := b.KVPut(ctx, "cfg", key, value, 0); err != nil {
			fatalf("put %s: %v", key, err)
		}
		fmt.Printf("seeded %s (%d bytes)\n", key, len(value))
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+f+"\n", a...)
	os.Exit(2)
}
