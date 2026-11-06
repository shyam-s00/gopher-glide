package mcp

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func getPrompt(t *testing.T, cs *sdk.ClientSession, name string, args map[string]string) (string, error) {
	t.Helper()
	res, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	if len(res.Messages) != 1 {
		t.Fatalf("%s: messages = %d", name, len(res.Messages))
	}
	return res.Messages[0].Content.(*sdk.TextContent).Text, nil
}

func fullArgs(p *sdk.Prompt) map[string]string {
	args := map[string]string{}
	for _, a := range p.Arguments {
		args[a.Name] = "VALUE_" + a.Name
	}
	return args
}

func TestPrompts_ListedWithDescriptions(t *testing.T) {
	cs := connect(t)
	list, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Prompts) < 2 {
		t.Fatalf("want at least 2 prompts, got %d", len(list.Prompts))
	}
	for _, p := range list.Prompts {
		if p.Description == "" || len(p.Arguments) == 0 {
			t.Errorf("prompt %q needs a description and arguments", p.Name)
		}
	}
}

func TestPrompts_RenderArgumentsAndRequireMandatoryOnes(t *testing.T) {
	cs := connect(t)
	for _, d := range promptDefs() {
		t.Run(d.prompt.Name, func(t *testing.T) {
			text, err := getPrompt(t, cs, d.prompt.Name, fullArgs(d.prompt))
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range d.prompt.Arguments {
				if a.Required && !strings.Contains(text, "VALUE_"+a.Name) {
					t.Errorf("required argument %q not used in the prompt text", a.Name)
				}
			}
			for _, a := range d.prompt.Arguments {
				if !a.Required {
					continue
				}
				args := fullArgs(d.prompt)
				delete(args, a.Name)
				if _, err := getPrompt(t, cs, d.prompt.Name, args); err == nil {
					t.Errorf("missing %q should be an error", a.Name)
				}
			}
		})
	}
}

var snakeWord = regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)

// steerActions are parameters of steer_traffic that prompts may name.
var steerActions = []string{"adjust_rps", "set_rps"}

// Prompt text may name tools only if the server really offers them.
func TestPrompts_OnlyReferenceKnownTools(t *testing.T) {
	for _, d := range promptDefs() {
		known := slices.Clone(toolNames)
		known = append(known, steerActions...)
		for _, a := range d.prompt.Arguments {
			known = append(known, a.Name)
		}
		text := d.build(fullArgs(d.prompt))
		for _, w := range snakeWord.FindAllString(text, -1) {
			if slices.Contains(known, w) || strings.HasPrefix(w, "value_") {
				continue
			}
			t.Errorf("prompt %q mentions %q, which is neither a tool nor an argument", d.prompt.Name, w)
		}
	}
}
