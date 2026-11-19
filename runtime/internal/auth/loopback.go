package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type callback struct {
	code string
	err  error
}

// signIn runs the Supabase PKCE flow with a one-shot listener on a random
// 127.0.0.1 port and returns the user's access token.
func (c Client) signIn(ctx context.Context, sb supabase, in LoginInput) (string, error) {
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	verifier, challenge := newPKCE()
	state := randomString(32)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start the login callback listener: %w", err)
	}
	results := make(chan callback, 1)
	srv := &http.Server{Handler: callbackHandler(state, results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()

	redirect := (&url.URL{Scheme: "http", Host: ln.Addr().String(), Path: "/callback",
		RawQuery: url.Values{"state": {state}}.Encode()}).String()

	if in.Email != "" {
		if err := sb.sendMagicLink(ctx, in.Email, redirect, challenge); err != nil {
			return "", fmt.Errorf("send the sign-in link: %w", err)
		}
		_, _ = fmt.Fprintf(c.Out, "Sent a sign-in link to %s. Open it in a browser on this machine to finish.\n", in.Email)
	} else {
		authURL := sb.authorizeURL(in.Provider, redirect, challenge)
		_, _ = fmt.Fprintf(c.Out, "Opening your browser to sign in. If it does not open, visit:\n\n  %s\n\n", authURL)
		if err := c.Open(authURL); err != nil {
			_, _ = fmt.Fprintf(c.Out, "Could not open a browser (%v); open the URL above yourself.\n", err)
		}
	}
	_, _ = fmt.Fprintf(c.Out, "Waiting for sign-in (up to %s)...\n", timeout)

	var res callback
	select {
	case res = <-results:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("sign-in timed out after %s", timeout)
		}
		return "", ctx.Err()
	}
	if res.err != nil {
		return "", res.err
	}
	return sb.exchange(ctx, res.code, verifier)
}

// callbackHandler accepts the first redirect carrying the expected state.
// Requests with any other state are refused without ending the wait, so a
// stray or forged request cannot abort or hijack the login.
func callbackHandler(state string, results chan<- callback) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "This sign-in link does not belong to the running agenthub login.", http.StatusBadRequest)
			return
		}
		var res callback
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("sign-in failed: %s: %s", printable(q.Get("error")), printable(q.Get("error_description")))
		case q.Get("code") == "":
			res.err = errors.New("sign-in failed: Supabase returned no authorization code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if res.err != nil {
			_, _ = fmt.Fprintln(w, "Konduite sign-in failed. Check the terminal for details.")
		} else {
			_, _ = fmt.Fprintln(w, "Signed in to Konduite. You can close this tab and return to the terminal.")
		}
		select {
		case results <- res:
		default:
		}
	})
	return mux
}

func newPKCE() (verifier, challenge string) {
	verifier = randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
