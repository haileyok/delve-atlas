// Package identity resolves a DID to its handle, PDS and Delve profile.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Resolver looks DIDs up over HTTP.
type Resolver struct {
	PLC  string // default https://plc.directory
	HTTP *http.Client
}

// New returns a resolver with defaults.
func New() *Resolver {
	return &Resolver{PLC: "https://plc.directory", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Actor is what we learn about one account.
type Actor struct {
	DID         string
	Handle      string
	PDS         string
	DisplayName string
	Description string
}

type didDoc struct {
	AlsoKnownAs []string `json:"alsoKnownAs"`
	Service     []struct {
		ID              string `json:"id"`
		Type            string `json:"type"`
		ServiceEndpoint string `json:"serviceEndpoint"`
	} `json:"service"`
}

func (r *Resolver) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Resolve fetches the DID document and, when the account's PDS is reachable, its Delve profile.
func (r *Resolver) Resolve(ctx context.Context, did string) (Actor, error) {
	a := Actor{DID: did}
	var doc didDoc
	var err error
	switch {
	case strings.HasPrefix(did, "did:plc:"):
		err = r.get(ctx, r.PLC+"/"+did, &doc)
	case strings.HasPrefix(did, "did:web:"):
		host := strings.TrimPrefix(did, "did:web:")
		err = r.get(ctx, "https://"+strings.ReplaceAll(host, "%3A", ":")+"/.well-known/did.json", &doc)
	default:
		err = fmt.Errorf("unsupported DID method: %s", did)
	}
	if err != nil {
		return a, err
	}
	for _, aka := range doc.AlsoKnownAs {
		if h, ok := strings.CutPrefix(aka, "at://"); ok {
			// "handle.invalid" is atproto's placeholder for a handle that doesn't verify.
			if h != "handle.invalid" {
				a.Handle = h
			}
			break
		}
	}
	for _, s := range doc.Service {
		if s.Type == "AtprotoPersonalDataServer" || strings.HasSuffix(s.ID, "#atproto_pds") {
			a.PDS = s.ServiceEndpoint
			break
		}
	}
	if a.PDS != "" {
		var rec struct {
			Value struct {
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"value"`
		}
		q := url.Values{"repo": {did}, "collection": {"town.delve.actor.profile"}, "rkey": {"self"}}
		if err := r.get(ctx, strings.TrimRight(a.PDS, "/")+"/xrpc/com.atproto.repo.getRecord?"+q.Encode(), &rec); err == nil {
			a.DisplayName = rec.Value.DisplayName
			a.Description = rec.Value.Description
		}
	}
	return a, nil
}
