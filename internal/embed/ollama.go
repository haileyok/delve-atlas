// Package embed turns post text into vectors with a local Ollama server.
package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Client talks to Ollama's /api/embed.
type Client struct {
	BaseURL string // default http://127.0.0.1:11434
	Model   string // default nomic-embed-text
	// Key is what the embeddings table records as the model: Model plus any text-recipe
	// version. Defaults to Model.
	Key string
	// Prefix is prepended to every input. nomic-embed-text expects a task prefix; "clustering: "
	// is the one meant for grouping documents.
	Prefix string
	HTTP   *http.Client
}

// New returns a client with defaults filled in.
func New(baseURL, model string) *Client {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	return &Client{
		BaseURL: baseURL, Model: model, Prefix: "clustering: ",
		HTTP: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Embed returns one L2-normalised vector per input.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	in := make([]string, len(inputs))
	for i, s := range inputs {
		in[i] = c.Prefix + s
	}
	body, _ := json.Marshal(map[string]any{"model": c.Model, "input": in, "truncate": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama embed: HTTP %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama embed: got %d vectors for %d inputs", len(out.Embeddings), len(inputs))
	}
	for _, v := range out.Embeddings {
		normalize(v)
	}
	return out.Embeddings, nil
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
}

// Pack encodes a vector as little-endian float32.
func Pack(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}
