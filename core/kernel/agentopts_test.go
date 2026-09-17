package kernel_test

import (
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

// configurableAgent implements the exported setters the (fixed) kernel
// options assert on.
type configurableAgent struct {
	name        string
	description string
	system      string
	maxSteps    int
	inSchema    map[string]any
	outSchema   map[string]any
}

func (a *configurableAgent) SetName(n string)                      { a.name = n }
func (a *configurableAgent) SetDescription(d string)               { a.description = d }
func (a *configurableAgent) SetSystemPrompt(p string)              { a.system = p }
func (a *configurableAgent) SetMaxSteps(n int)                     { a.maxSteps = n }
func (a *configurableAgent) SetInputSchema(s map[string]any)       { a.inSchema = s }
func (a *configurableAgent) SetOutputSchema(s map[string]any)      { a.outSchema = s }

// Regression: the AgentOption functions asserted on UNEXPORTED setters
// (setName...), which no type outside package kernel can satisfy — every
// documented option silently no-oped (C1). They now assert exported
// setters and must actually apply.
func TestAgentOptions_ApplyToExportedSetters(t *testing.T) {
	a := &configurableAgent{}
	var v any = a

	kernel.WithName("agent-1")(v)
	kernel.WithDescription("desc")(v)
	kernel.WithSystemPrompt("sys")(v)
	kernel.WithMaxSteps(7)(v)
	in := map[string]any{"type": "object"}
	out := map[string]any{"type": "object"}
	kernel.WithInputSchema(in)(v)
	kernel.WithOutputSchema(out)(v)

	if a.name != "agent-1" || a.description != "desc" || a.system != "sys" || a.maxSteps != 7 {
		t.Fatalf("options did not apply: %+v", a)
	}
	if a.inSchema == nil || a.outSchema == nil {
		t.Fatalf("schema options did not apply: %+v", a)
	}
}
