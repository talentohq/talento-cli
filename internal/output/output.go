package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/itchyny/gojq"
	"github.com/talentohq/talento-cli/internal/terminal"
)

type Options struct {
	JSON      bool
	Markdown  bool
	Agent     bool
	JQ        string
	Writer    io.Writer
	ErrWriter io.Writer
}

type Writer struct {
	opts Options
}

type HumanRenderable interface {
	HumanText() string
}

type errorData interface {
	ErrorData() any
}

type renderedErrorData interface {
	RenderedErrorData() any
}

type RichError struct {
	Base *baseoutput.Error
	Data any
}

type renderedRichError struct {
	*RichError
}

func (e *RichError) Error() string  { return e.Base.Error() }
func (e *RichError) Unwrap() error  { return e.Base }
func (e *RichError) ErrorData() any { return e.Data }

func (e *renderedRichError) RenderedErrorData() any { return e.Data }

func New(opts Options) *Writer {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.ErrWriter == nil {
		opts.ErrWriter = os.Stderr
	}
	return &Writer{opts: opts}
}

func (w *Writer) Success(data any, summary string, breadcrumbs []baseoutput.Breadcrumb, meta map[string]any) error {
	resp := baseoutput.Response{
		OK:          true,
		Data:        data,
		Summary:     summary,
		Breadcrumbs: breadcrumbs,
		Meta:        meta,
	}

	if w.opts.JQ != "" {
		input := any(resp)
		if w.opts.Agent {
			input = data
		}
		filtered, err := applyJQ(input, w.opts.JQ)
		if err != nil {
			return baseoutput.ErrUsageHint("invalid --jq expression", err.Error())
		}
		return writeJSON(w.opts.Writer, filtered)
	}
	if w.opts.Agent {
		return writeJSON(w.opts.Writer, data)
	}
	if w.opts.JSON {
		return writeJSON(w.opts.Writer, resp)
	}
	if w.opts.Markdown {
		return w.writeMarkdown(data, summary)
	}
	return w.writeHuman(data, summary)
}

func (w *Writer) Error(err error) error {
	e := baseoutput.AsError(err)
	resp := struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		} `json:"error"`
		Data any `json:"data,omitempty"`
	}{OK: false}
	resp.Error.Code = e.Code
	resp.Error.Message = e.Message
	resp.Error.Hint = e.Hint
	var rich errorData
	if errors.As(err, &rich) {
		resp.Data = rich.ErrorData()
	}

	if w.opts.JSON || w.opts.Agent || w.opts.JQ != "" {
		return writeJSON(w.opts.ErrWriter, resp)
	}
	var rendered renderedErrorData
	if errors.As(err, &rendered) {
		if wrote, writeErr := writeHumanData(w.opts.ErrWriter, rendered.RenderedErrorData()); wrote || writeErr != nil {
			return writeErr
		}
	}
	if e.Hint != "" {
		_, writeErr := fmt.Fprintf(w.opts.ErrWriter, "Error: %s\nHint: %s\n", terminal.SanitizeLine(e.Message), terminal.SanitizeLine(e.Hint))
		return writeErr
	}
	_, writeErr := fmt.Fprintf(w.opts.ErrWriter, "Error: %s\n", terminal.SanitizeLine(e.Message))
	return writeErr
}

func ExitCode(err error) int {
	return baseoutput.AsError(err).ExitCode()
}

func Usage(message, hint string) error {
	return baseoutput.ErrUsageHint(message, hint)
}

func Auth(message string) error {
	e := baseoutput.ErrAuth(message)
	e.Hint = "Run `talento auth login`."
	return e
}

func NotFound(resource, identifier string) error {
	return baseoutput.ErrNotFound(resource, identifier)
}

func Forbidden(message string) error {
	return baseoutput.ErrForbidden(message)
}

func Network(err error) error {
	return baseoutput.ErrNetwork(err)
}

func API(message string, cause error) error {
	e := baseoutput.ErrAPI(0, message)
	e.Cause = cause
	return e
}

func WithData(err error, data any) error {
	return &RichError{Base: baseoutput.AsError(err), Data: data}
}

// WithRenderedData attaches structured error data and uses its human rendering
// as the complete error output in human mode. JSON modes retain the standard
// error envelope and include the data under the data key.
func WithRenderedData(err error, data any) error {
	return &renderedRichError{RichError: &RichError{Base: baseoutput.AsError(err), Data: data}}
}

func (w *Writer) writeHuman(data any, summary string) error {
	if wrote, err := writeHumanData(w.opts.Writer, data); wrote || err != nil {
		return err
	}
	if summary != "" {
		if _, err := fmt.Fprintln(w.opts.Writer, terminal.Sanitize(summary)); err != nil {
			return err
		}
	}
	return writeJSON(w.opts.Writer, data)
}

func writeHumanData(writer io.Writer, data any) (bool, error) {
	if renderable, ok := data.(HumanRenderable); ok {
		text := strings.TrimSpace(renderable.HumanText())
		if text != "" {
			_, err := fmt.Fprintln(writer, terminal.Sanitize(text))
			return true, err
		}
	}
	if text, ok := data.(string); ok {
		_, err := fmt.Fprintln(writer, terminal.Sanitize(strings.TrimSpace(text)))
		return true, err
	}
	return false, nil
}

func (w *Writer) writeMarkdown(data any, summary string) error {
	if summary != "" {
		heading := terminal.SanitizeLine(strings.TrimSuffix(strings.TrimSpace(summary), "."))
		if _, err := fmt.Fprintf(w.opts.Writer, "## %s\n\n", heading); err != nil {
			return err
		}
	}
	if renderable, ok := data.(HumanRenderable); ok {
		text := strings.TrimSpace(renderable.HumanText())
		if text != "" {
			_, err := fmt.Fprintln(w.opts.Writer, terminal.Sanitize(text))
			return err
		}
	}
	encoded, err := marshalTerminalJSON(data, true)
	if err != nil {
		return err
	}
	encoded = bytes.TrimSuffix(encoded, []byte{'\n'})
	_, err = fmt.Fprintf(w.opts.Writer, "```json\n%s\n```\n", encoded)
	return err
}

func writeJSON(w io.Writer, value any) error {
	data, err := marshalTerminalJSON(value, true)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// WriteJSON writes the CLI's safe, stable structured representation. It is
// exported for the small number of machine-readable command surfaces that do not
// pass through Success or Error.
func WriteJSON(w io.Writer, value any) error {
	return writeJSON(w, value)
}

func applyJQ(value any, expression string) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	query, err := gojq.Parse(expression)
	if err != nil {
		return nil, err
	}
	iter := query.Run(normalized)
	results := make([]any, 0, 1)
	for {
		item, ok := iter.Next()
		if !ok {
			break
		}
		if runErr, ok := item.(error); ok {
			return nil, runErr
		}
		results = append(results, item)
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}
