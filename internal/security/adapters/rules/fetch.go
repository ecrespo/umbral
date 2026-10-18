package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// The fetcher's bounds. A bundle is a few kilobytes of rules; the cap only stops a hostile
// endpoint from filling memory.
const (
	MaxBundleBytes = 4 << 20
	CheckInterval  = 24 * time.Hour
	maxRedirects   = 5
	fetchTimeout   = 30 * time.Second
	// EgressProvider is the `provider` of the fetcher's egress_log rows.
	EgressProvider = "umbral-rules"
)

// FetchConfig is what the fetcher needs.
type FetchConfig struct {
	// Enabled is `[update] rules_check`. Off, Check makes no request (Art. 4).
	Enabled bool
	// Allowed reports whether remote updates are open; false — failed closed (REQ-SEC-015) —
	// makes a check request nothing. Nil is always open.
	Allowed func(context.Context) bool
	// Redact is the active redactor. A URL it would change carries what looks like a secret
	// and is refused (Art. 4), as fetch_url refuses one. Nil checks nothing.
	Redact func(string) string
	// URL is `[update] rules_url`; the signature is URL + ".sig".
	URL     string
	Client  *http.Client
	Egress  ports.RuleEgress
	Updater ports.RuleUpdater
	Logger  *slog.Logger
	Now     func() time.Time
}

// Fetcher brings signed rule bundles in (REQ-SEC-011) and hands them to the updater.
type Fetcher struct {
	cfg FetchConfig
}

// NewFetcher builds the fetcher.
func NewFetcher(cfg FetchConfig) (*Fetcher, error) {
	switch {
	case cfg.Egress == nil:
		return nil, errors.New("rules: an Egress log is required")
	case cfg.Updater == nil:
		return nil, errors.New("rules: an Updater is required")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: fetchTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Enabled && cfg.Redact != nil {
		for _, u := range []string{cfg.URL, cfg.URL + ".sig"} {
			if cfg.Redact(u) != u {
				return nil, errors.New("rules: rules_url carries what looks like a secret; it is not fetched")
			}
		}
	}
	f := &Fetcher{cfg: cfg}
	// A copy, so the caller's client keeps its own redirect policy.
	client := *cfg.Client
	client.CheckRedirect = f.redirect
	f.cfg.Client = &client
	return f, nil
}

// redirect records each hop before it is followed: the default rules_url answers with a
// redirect to another host, and that request is an egress of its own (Art. 4). A hop that
// leaves https is refused.
func (f *Fetcher) redirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("rules: more than %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("rules: a redirect to %s is not https", req.URL.Host)
	}
	return f.record(req.Context(), req.URL)
}

// Run checks at once and then every CheckInterval until ctx ends. Off, it returns at once.
func (f *Fetcher) Run(ctx context.Context) {
	if !f.cfg.Enabled {
		return
	}
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		f.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check fetches the bundle and its signature once and offers them to the updater. A fetch
// that fails — network, HTTP error, too large — is logged and left for the next check: it is
// not a rejected bundle (DD-016).
func (f *Fetcher) Check(ctx context.Context) {
	if !f.cfg.Enabled {
		return
	}
	if f.cfg.Allowed != nil && !f.cfg.Allowed(ctx) {
		f.cfg.Logger.DebugContext(ctx, "remote rule updates are failed closed; nothing is fetched")
		return
	}
	bundle, err := f.get(ctx, f.cfg.URL)
	if err != nil {
		f.cfg.Logger.WarnContext(ctx, "the rule bundle could not be fetched; retrying at the next check", slog.Any("error", err))
		return
	}
	sig, err := f.get(ctx, f.cfg.URL+".sig")
	if err != nil {
		f.cfg.Logger.WarnContext(ctx, "the rule bundle's signature could not be fetched; retrying at the next check", slog.Any("error", err))
		return
	}
	f.cfg.Updater.Offer(ctx, bundle, sig)
}

// get makes one request, recorded in egress_log before it is sent: what leaves is the
// request line, so its size and digest are the URL's.
func (f *Fetcher) get(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("rules: the URL: %w", err)
	}
	if err := f.record(ctx, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.cfg.Client.Do(req)
	if err != nil {
		// A *url.Error names the URL, and a redirect target can carry signed query
		// parameters: say the host and the cause only (Art. 7).
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("rules: the request to %s failed: %w", u.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rules: %s answered %s", u.Host, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBundleBytes {
		return nil, fmt.Errorf("rules: %s sent more than %d bytes", u.Host, MaxBundleBytes)
	}
	return data, nil
}

// record writes one request's egress_log row. A request Art. 4 cannot record is not made.
func (f *Fetcher) record(ctx context.Context, u *url.URL) error {
	raw := u.String()
	digest := sha256.Sum256([]byte(raw))
	if err := f.cfg.Egress.Record(ctx, llmdomain.EgressRecord{
		Provider: EgressProvider, Host: u.Host, Bytes: int64(len(raw)),
		PayloadSHA256: hex.EncodeToString(digest[:]), CreatedAt: f.cfg.Now().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("rules: record the egress: %w", err)
	}
	return nil
}
