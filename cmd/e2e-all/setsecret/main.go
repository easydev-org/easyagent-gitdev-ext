// Set a repository secret, using the sealed-box scheme GitHub requires.
//
// WHY THIS EXISTS RATHER THAN `gh secret set`: the e2e suite needs a GitHub token in CI, and a
// repository that has never had one cannot run half its tools. The alternative — clicking through the
// settings page — is a step nobody can repeat or review.
//
// THE ENCRYPTION IS NOT OPTIONAL. GitHub will not accept a secret in the clear: the value must be
// sealed with the repository's public key using libsodium's sealed-box construction, which is what
// x/crypto/nacl/box implements.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
)

func main() {
	var (
		repo  = flag.String("repo", envOr("SECRET_REPO", ""), "owner/name")
		name  = flag.String("name", envOr("SECRET_NAME", ""), "secret name")
		value = flag.String("value", os.Getenv("SECRET_VALUE"), "secret value (or SECRET_VALUE)")
		token = flag.String("token", os.Getenv("GH_TOKEN"), "admin token for the repository")
	)
	flag.Parse()
	if *repo == "" || *name == "" || *value == "" || *token == "" {
		fatalf("repo, name, value and token are all required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The repository's public key. It rotates, so it is fetched per call rather than cached.
	keyURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/secrets/public-key", *repo)
	var pub struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := doJSON(ctx, http.MethodGet, keyURL, *token, nil, &pub); err != nil {
		fatalf("read the repository's public key: %v", err)
	}

	rawKey, err := base64.StdEncoding.DecodeString(pub.Key)
	if err != nil {
		fatalf("decode the public key: %v", err)
	}
	var pk [32]byte
	copy(pk[:], rawKey)

	// SEALED BOX: the recipient's public key and an ephemeral sender key, which is exactly the shape
	// GitHub documents. The 32-byte ephemeral public half is PREPENDED to the box by box.SealAnonymous,
	// which is what libsodium's crypto_box_seal does.
	sealed, err := box.SealAnonymous(nil, []byte(*value), &pk, randReader{})
	if err != nil {
		fatalf("seal the value: %v", err)
	}

	putURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/secrets/%s", *repo, *name)
	body := map[string]string{
		"encrypted_value": base64.StdEncoding.EncodeToString(sealed),
		"key_id":          pub.KeyID,
	}
	if err := doJSON(ctx, http.MethodPut, putURL, *token, body, nil); err != nil {
		fatalf("write the secret: %v", err)
	}
	fmt.Printf("set %s on %s\n", *name, *repo)
}

// randReader is crypto/rand as an interface, kept explicit so the import list says why it is there.
type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return rand.Read(p) }

func doJSON(ctx context.Context, method, url, token string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "setsecret: "+f+"\n", a...)
	os.Exit(2)
}
