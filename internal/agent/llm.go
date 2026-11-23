package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

// DefaultModel is the Gemini model used when none is chosen. The alias tracks
// the current Flash-tier model.
const DefaultModel = "gemini-flash-latest"

// Environment variables that configure the model.
const (
	EnvModel   = "GG_AUTOPILOT_MODEL"
	EnvBaseURL = "GG_AUTOPILOT_BASE_URL"
	EnvAPIKey  = "GG_AUTOPILOT_API_KEY"
)

// ErrNoModel means no LLM is configured; slash commands still work without one.
var ErrNoModel = errors.New("no LLM configured: set GEMINI_API_KEY (Gemini), or --base-url with --model for an OpenAI-compatible server such as Ollama")

// ModelConfig selects the LLM.
type ModelConfig struct {
	// Name is the model; default DefaultModel for Gemini, required with BaseURL.
	Name string
	// BaseURL is an OpenAI-compatible chat completions endpoint, e.g. http://localhost:11434/v1.
	BaseURL string
	// APIKey authenticates to BaseURL; many local servers accept any value.
	APIKey string
}

// ModelConfigFromEnv fills unset fields from the environment.
func ModelConfigFromEnv(c ModelConfig) ModelConfig {
	if c.Name == "" {
		c.Name = os.Getenv(EnvModel)
	}
	if c.BaseURL == "" {
		c.BaseURL = os.Getenv(EnvBaseURL)
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv(EnvAPIKey)
	}
	return c
}

func geminiKey() string {
	if k := os.Getenv("GEMINI_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("GOOGLE_API_KEY")
}

func useVertex() bool {
	v := os.Getenv("GOOGLE_GENAI_USE_VERTEXAI")
	return strings.EqualFold(v, "true") || v == "1"
}

// HasModel reports whether the configuration can reach an LLM.
func HasModel(c ModelConfig) bool {
	return c.BaseURL != "" || geminiKey() != "" || useVertex()
}

// NewModel builds the LLM: an OpenAI-compatible server when BaseURL is set,
// otherwise Gemini (API key or Vertex AI). It returns ErrNoModel if neither is configured.
func NewModel(ctx context.Context, c ModelConfig) (model.LLM, error) {
	switch {
	case c.BaseURL != "":
		if c.Name == "" {
			return nil, errors.New("--model is required with --base-url")
		}
		return newOpenAIModel(c.Name, c.BaseURL, c.APIKey, nil), nil
	case geminiKey() != "":
		return newGemini(ctx, c.Name, &genai.ClientConfig{APIKey: geminiKey(), Backend: genai.BackendGeminiAPI})
	case useVertex():
		return newGemini(ctx, c.Name, &genai.ClientConfig{Backend: genai.BackendVertexAI})
	}
	return nil, ErrNoModel
}

func newGemini(ctx context.Context, name string, cfg *genai.ClientConfig) (model.LLM, error) {
	if name == "" {
		name = DefaultModel
	}
	m, err := gemini.NewModel(ctx, name, cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini model %q: %w", name, err)
	}
	return m, nil
}
