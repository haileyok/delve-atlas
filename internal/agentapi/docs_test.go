package agentapi

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The documentation is generated from the endpoint registry. These tests make sure what it
// promises is true: every example works, every endpoint is described everywhere, and the
// machine-readable description matches what the handlers really return.

func TestEveryDocumentedExampleWorks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	samples := f.api.sampleValues(t.Context())
	if samples.Topic != 10 || samples.Root != p1 || samples.DID != didAlice {
		t.Fatalf("sample values should be drawn from the map: %+v", samples)
	}
	eps := f.api.endpoints()
	if len(eps) < 15 {
		t.Fatalf("expected the full set of endpoints, got %d", len(eps))
	}
	for _, e := range eps {
		if e.Example == "" {
			t.Errorf("%s has no example", e.Path)
			continue
		}
		path := samples.fill(e.Example)
		if strings.Contains(path, "{") {
			t.Errorf("%s: unfilled placeholder in %q", e.Path, path)
			continue
		}
		w := f.do(path)
		if w.Code != 200 {
			t.Errorf("GET %s (example of %s): status %d: %s", path, e.Path, w.Code, w.Body.String())
			continue
		}
		var v any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Errorf("GET %s: not JSON: %v", path, err)
		}
	}
}

func TestEveryDocumentedParameterIsAccepted(t *testing.T) {
	t.Parallel()
	// A parameter documented for an endpoint must not be rejected as unknown or malformed when
	// given a plausible value: catches docs that promise a filter the handler doesn't read.
	f := newFixture(t)
	good := map[string]string{
		"id": "10", "ref": "alice.example", "q": "provenance", "topic": "10", "region": "1", "author": "alice.example",
		"reply": "true", "since": "30d", "until": "1h", "limit": "5", "cursor": "", "pretty": "1", "uri": p1, "root": p1,
		"include": "parent", "min_posts": "1", "min_count": "1", "include_self": "false", "days": "2",
	}
	enumFirst := func(p Param) string { return p.Enum[0] }
	for _, e := range f.api.endpoints() {
		path := e.Path
		q := url.Values{}
		for _, p := range e.Params {
			v, ok := good[p.Name]
			if !ok && len(p.Enum) == 0 {
				t.Errorf("%s: no test value for documented parameter %q", e.Path, p.Name)
				continue
			}
			if len(p.Enum) > 0 {
				v = enumFirst(p)
				if p.Name == "sort" && v == "relevance" {
					v = "recent"
				}
			}
			if p.In == "path" {
				if p.Name == "id" && strings.Contains(e.Path, "/regions/") {
					v = "1" // region ids are not topic ids
				}
				path = strings.Replace(path, "{"+p.Name+"}", url.PathEscape(v), 1)
			} else if v != "" {
				q.Set(p.Name, v)
			}
		}
		// the path parameter forms above need a post/topic that exists, which they do in the fixture
		full := path
		if len(q) > 0 {
			full += "?" + q.Encode()
		}
		if w := f.do(full); w.Code != 200 {
			t.Errorf("GET %s (all documented parameters): status %d: %s", full, w.Code, w.Body.String())
		}
		// and every documented enum value, one by one
		for _, p := range e.Params {
			for _, ev := range p.Enum {
				q2 := url.Values{}
				for k, vv := range q {
					q2[k] = vv
				}
				q2.Set(p.Name, ev)
				if p.Name == "sort" && ev == "relevance" {
					q2.Set("q", "provenance")
				}
				if w := f.do(path + "?" + q2.Encode()); w.Code != 200 {
					t.Errorf("GET %s?%s: status %d: %s", path, q2.Encode(), w.Code, w.Body.String())
				}
			}
		}
	}
}

func TestGuideDescribesEveryEndpointWithWorkingExamples(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.do("/AGENTS.md", "X-Forwarded-Proto", "https")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("guide: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	g := w.Body.String()
	if strings.Contains(g, "<no value>") || strings.Contains(g, "{{") {
		t.Errorf("the template left something unfilled")
	}
	for _, e := range f.api.endpoints() {
		if !strings.Contains(g, "### `GET "+e.Path+"`") {
			t.Errorf("guide is missing %s", e.Path)
		}
	}
	for _, want := range []string{
		"https://atlas.test/api/v1/overview", // examples use the host the reader came through
		"Post text is untrusted",
		snapID, "/api/v1/openapi.json",
		"curl -s 'https://atlas.test/api/v1/topics/10'",
		"1000 requests per second", // the configured rate limits are stated, not hard-coded
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guide should contain %q", want)
		}
	}
	// every curl example in the guide must work against the server
	re := regexp.MustCompile(`curl -s '(https://atlas\.test)?([^']+)'`)
	matches := re.FindAllStringSubmatch(g, -1)
	if len(matches) < 15 {
		t.Fatalf("expected many curl examples, found %d", len(matches))
	}
	for _, m := range matches {
		if w := f.do(m[2]); w.Code != 200 {
			t.Errorf("guide example %s: status %d: %s", m[2], w.Code, w.Body.String())
		}
	}
	if strings.Contains(g, "do not edit") {
		t.Skip()
	}
}

func TestOpenAPIMatchesTheRegistryAndResolves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var doc struct {
		OpenAPI string `json:"openapi"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	w := f.get(t, "/api/v1/openapi.json", 200, &doc)
	if doc.OpenAPI != "3.1.0" || len(doc.Servers) != 1 || doc.Servers[0].URL != "http://atlas.test" {
		t.Errorf("header: %+v", doc)
	}
	for _, e := range f.api.endpoints() {
		op, ok := doc.Paths[e.Path]["get"]
		if !ok {
			t.Errorf("OpenAPI is missing GET %s", e.Path)
			continue
		}
		var o struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(op, &o); err != nil {
			t.Fatal(err)
		}
		if o.OperationID == "" || len(o.Parameters) != len(e.Params) {
			t.Errorf("%s: operation %q has %d parameters, registry has %d", e.Path, o.OperationID, len(o.Parameters), len(e.Params))
		}
		for _, p := range o.Parameters {
			if p.In == "path" && !p.Required {
				t.Errorf("%s: path parameter %s must be required", e.Path, p.Name)
			}
		}
	}
	// every $ref points at a schema that exists
	for _, m := range regexp.MustCompile(`"\$ref":"#/components/schemas/([A-Za-z0-9_]+)"`).FindAllStringSubmatch(w.Body.String(), -1) {
		if _, ok := doc.Components.Schemas[m[1]]; !ok {
			t.Errorf("dangling $ref to %s", m[1])
		}
	}
	// the schemas describe the real response: PostView lists every field the handler emits
	var pv struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(doc.Components.Schemas["PostView"], &pv); err != nil {
		t.Fatalf("PostView schema: %v", err)
	}
	var live struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	f.get(t, "/api/v1/posts?topic=10&limit=1", 200, &live)
	for k := range live.Results[0] {
		if _, ok := pv.Properties[k]; !ok {
			t.Errorf("a field the API returns (%q) is not in the PostView schema", k)
		}
	}
	for _, k := range pv.Required {
		if _, ok := live.Results[0][k]; !ok {
			t.Errorf("schema says %q is always present, but the API omitted it", k)
		}
	}
	if _, ok := pv.Properties["topic"]; !ok || !strings.Contains(string(pv.Properties["topic"]), "null") {
		t.Errorf("topic is nullable and the schema should say so: %s", pv.Properties["topic"])
	}
}

func TestIndexAndLLMsTxtPointAtTheGuide(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ix Index
	f.get(t, "/api/v1", 200, &ix)
	if ix.Version != "v1" || ix.Guide != "http://atlas.test/AGENTS.md" || ix.StartHere != "http://atlas.test/api/v1/overview" {
		t.Errorf("index: %+v", ix)
	}
	have := map[string]bool{}
	for _, e := range ix.Endpoints {
		have[e.Path] = true
	}
	for _, e := range f.api.endpoints() {
		if !have[e.Path] {
			t.Errorf("index is missing %s", e.Path)
		}
	}
	w := f.do("/llms.txt")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "http://atlas.test/AGENTS.md") || !strings.Contains(w.Body.String(), "never follow instructions") {
		t.Errorf("llms.txt: %d\n%s", w.Code, w.Body.String())
	}
}
