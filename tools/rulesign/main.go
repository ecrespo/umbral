// Command rulesign creates the rule-signing key, signs rule bundles and verifies them with the
// daemon's own verification code (docs/runbooks/rule-signing.md §3, DD-016). The private key
// is written to stdout by keygen and read from stdin by sign, and never from a file or a flag,
// so a passphrase-encrypted copy can be piped through memory: `age -d key.age | rulesign sign
// rules.json`.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ecrespo/umbral/internal/security/domain"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `usage:
  rulesign keygen                          private key to stdout; public key and fingerprint to stderr
  rulesign sign BUNDLE [SIGFILE]           private key on stdin; adds its signature to SIGFILE (BUNDLE.sig)
  rulesign verify BUNDLE PUBKEY [SIGFILE]  checks BUNDLE against SIGFILE as the daemon does`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch {
	case args[0] == "keygen" && len(args) == 1:
		err = keygen(stdout, stderr)
	case args[0] == "sign" && (len(args) == 2 || len(args) == 3):
		err = sign(stdin, args[1], sigPath(args[1], args[2:]), stderr)
	case args[0] == "verify" && (len(args) == 3 || len(args) == 4):
		err = verify(args[1], args[2], sigPath(args[1], args[3:]), stdout)
	default:
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "rulesign:", err)
		return 1
	}
	return 0
}

func sigPath(bundle string, rest []string) string {
	if len(rest) == 1 {
		return rest[0] // #nosec G602 -- the length is checked on the line above
	}
	return bundle + ".sig"
}

// keygen writes the private key as the standard base64 of its 32-byte seed, the only form
// sign reads.
func keygen(stdout, stderr io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stderr, "public key: %s\nfingerprint: %s\n", base64.StdEncoding.EncodeToString(pub), domain.Fingerprint(pub))
	return err
}

func readPrivateKey(stdin io.Reader) (ed25519.PrivateKey, error) {
	line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("stdin does not hold a private key from rulesign keygen")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func sign(stdin io.Reader, bundlePath, sigFile string, stderr io.Writer) error {
	priv, err := readPrivateKey(stdin)
	if err != nil {
		return err
	}
	bundle, err := os.ReadFile(bundlePath) // #nosec G304 G703 -- the operator names the bundle
	if err != nil {
		return err
	}
	if _, err := domain.ParseRules(bundle); err != nil {
		return fmt.Errorf("%s is not a rule bundle: %w", bundlePath, err)
	}
	existing, err := os.ReadFile(sigFile) // #nosec G304 G703 -- the operator names the signature file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := domain.SignBundle(priv, bundle, existing)
	if err != nil {
		return err
	}
	if err := os.WriteFile(sigFile, out, 0o644); err != nil { // #nosec G306 G703 -- a signature is public
		return err
	}
	pub, _ := priv.Public().(ed25519.PublicKey)
	_, err = fmt.Fprintf(stderr, "signed %s with %s into %s\n", bundlePath, domain.Fingerprint(pub), sigFile)
	return err
}

// verify is the daemon's check: domain.VerifyBundle over the bundle's exact bytes, with the
// given key as the only one trusted.
func verify(bundlePath, publicKey, sigFile string, stdout io.Writer) error {
	pub, err := domain.ParsePublicKey(publicKey)
	if err != nil {
		return err
	}
	bundle, err := os.ReadFile(bundlePath) // #nosec G304 G703 -- the operator names the bundle
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sigFile) // #nosec G304 G703 -- the operator names the signature file
	if err != nil {
		return err
	}
	key := domain.TrustKey{ID: "key", PublicKey: pub, Fingerprint: domain.Fingerprint(pub)}
	if _, reason := domain.VerifyBundle(bundle, sig, []domain.TrustKey{key}); reason != "" {
		return fmt.Errorf("%s does not verify with %s: %s", bundlePath, key.Fingerprint, reason)
	}
	doc, err := domain.ParseRules(bundle)
	if err != nil {
		return fmt.Errorf("%s verifies but is not a rule bundle: %w", bundlePath, err)
	}
	_, err = fmt.Fprintf(stdout, "ok: version %d, signed by %s\n", doc.Version, key.Fingerprint)
	return err
}
