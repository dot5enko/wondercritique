package tool

// Terminal is a Tool whose execution ends the agent run: the tool's output string becomes the
// run's final answer and no further model call is made after it executes. When the call fails,
// the error text (which is what the run records as the tool result output) becomes the final
// answer instead, and the run still completes. Tools may implement this interface directly or
// be wrapped with AsTerminal.
type Terminal interface {
	Tool
	IsTerminal() bool
}

// terminalTool wraps a Tool so it satisfies Terminal, delegating everything else to the
// wrapped tool.
type terminalTool struct{ Tool }

// IsTerminal implements Terminal.
func (terminalTool) IsTerminal() bool { return true }

// AsTerminal wraps t in a terminal tool: when the model calls it, the wrapped tool's output
// string becomes the run's final answer and no further model call happens. If the call fails,
// the error text (which is what the run records as the tool result output) becomes the final
// answer instead, and the run still completes.
func AsTerminal(t Tool) Tool {
	return terminalTool{Tool: t}
}
