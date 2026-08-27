package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
)

const maxInputBytes = 8 << 20

const (
	schemaToolAnnotation = "talento.input_schema_tool"
	dynamicSchemaTool    = "<command-argument>"
)

type validatedInputKey struct{}

type validatedInput struct {
	tool      string
	arguments map[string]any
}

type schemaInput struct {
	rawJSON   string
	inputFile string
}

func addSchemaFlags(command *cobra.Command, tool schema.Tool) *schemaInput {
	input := &schemaInput{}
	command.Flags().StringVar(&input.rawJSON, "input", "", "JSON object merged with schema-derived flags")
	command.Flags().StringVar(&input.inputFile, "input-file", "", "path to a JSON object (use - for stdin)")
	for _, name := range schema.SortedProperties(tool) {
		property := tool.InputSchema.Properties[name]
		flagName := strings.ReplaceAll(name, "_", "-")
		description := propertyDescription(tool, name, property)
		switch property.PrimaryType() {
		case "boolean":
			command.Flags().Bool(flagName, false, description)
		default:
			// String-backed numeric and complex flags preserve the version-1 CLI
			// surface. Values are parsed to their JSON type before validation.
			command.Flags().String(flagName, "", description)
		}
		registerEnumCompletion(command, flagName, property.Enum)

		if item, ok := scalarArrayItem(property); ok {
			itemFlag := flagName + "-item"
			itemDescription := "append one " + item.PrimaryType() + " item (repeatable; literal commas are preserved)"
			command.Flags().StringArray(itemFlag, nil, itemDescription)
			registerEnumCompletion(command, itemFlag, item.Enum)
		}
	}
	return input
}

func propertyDescription(tool schema.Tool, name string, property schema.Property) string {
	description := strings.TrimSpace(property.Description)
	if schema.IsRequired(tool, name) {
		description = "(required; may be supplied by --input) " + description
	}
	if values := enumStrings(property.Enum); len(values) > 0 {
		description += " [" + strings.Join(values, ", ") + "]"
	}
	if property.PrimaryType() == "array" {
		description += " (JSON array; use [] for empty)"
	} else if property.PrimaryType() == "object" {
		description += " (JSON object)"
	}
	return strings.TrimSpace(description)
}

func registerEnumCompletion(command *cobra.Command, flagName string, values []any) {
	candidates := enumStrings(values)
	if len(candidates) == 0 {
		return
	}
	_ = command.RegisterFlagCompletionFunc(flagName, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return append([]string(nil), candidates...), cobra.ShellCompDirectiveNoFileComp
	})
}

func enumStrings(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		switch value := value.(type) {
		case string:
			result = append(result, value)
		case bool:
			result = append(result, strconv.FormatBool(value))
		case float64:
			result = append(result, strconv.FormatFloat(value, 'g', -1, 64))
		}
	}
	return result
}

func scalarArrayItem(property schema.Property) (schema.Property, bool) {
	if property.PrimaryType() != "array" {
		return schema.Property{}, false
	}
	item, ok := property.ItemProperty()
	if !ok {
		return schema.Property{}, false
	}
	switch item.PrimaryType() {
	case "string", "integer", "number", "boolean":
		return item, true
	default:
		return schema.Property{}, false
	}
}

func (i *schemaInput) arguments(command *cobra.Command, tool schema.Tool) (map[string]any, error) {
	arguments, err := i.sourceArguments(command)
	if err != nil {
		return nil, err
	}
	for _, name := range schema.SortedProperties(tool) {
		flagName := strings.ReplaceAll(name, "_", "-")
		property := tool.InputSchema.Properties[name]
		baseFlag := command.Flags().Lookup(flagName)
		baseChanged := baseFlag != nil && baseFlag.Changed
		item, hasItemFlag := scalarArrayItem(property)
		itemFlagName := flagName + "-item"
		itemFlag := command.Flags().Lookup(itemFlagName)
		itemChanged := itemFlag != nil && itemFlag.Changed
		if baseChanged && itemChanged {
			return nil, inputUsage(
				fmt.Sprintf("--%s and --%s are mutually exclusive", flagName, itemFlagName),
				"Use the repeatable item flag for scalar values or the base flag for one complete JSON array.",
			)
		}
		if itemChanged && hasItemFlag {
			rawValues, flagErr := command.Flags().GetStringArray(itemFlagName)
			if flagErr != nil {
				return nil, inputUsage("cannot read --"+itemFlagName, "Pass the flag once per array item.")
			}
			values := make([]any, 0, len(rawValues))
			for _, raw := range rawValues {
				value, parseErr := parseScalarFlag(raw, item.PrimaryType())
				if parseErr != nil {
					return nil, inputUsage(fmt.Sprintf("--%s must contain %s values", itemFlagName, item.PrimaryType()), "Pass the flag once per item; literal commas are not split.")
				}
				values = append(values, value)
			}
			arguments[name] = values
			continue
		}
		if !baseChanged {
			continue
		}
		value, parseErr := parseSchemaFlag(command, flagName, property.PrimaryType())
		if parseErr != nil {
			return nil, parseErr
		}
		arguments[name] = value
	}
	if err := tool.InputSchema.ValidateInput(arguments); err != nil {
		var validationErr *schema.ValidationError
		if errors.As(err, &validationErr) {
			usage := clioutput.Usage(validationErr.Error(), "Check this command's --help or pass a valid JSON object with --input.")
			return nil, clioutput.WithData(usage, validationErr)
		}
		return nil, fmt.Errorf("validate embedded input schema for %s: %w", tool.Name, err)
	}
	return arguments, nil
}

// preflightSchemaArguments runs from the root persistent pre-run hook, before
// interactive authentication can perform OAuth network operations. The parsed
// object is cached in the command context so stdin is never consumed twice.
func preflightSchemaArguments(command *cobra.Command, positional []string, talento *app.App) error {
	toolName := command.Annotations[schemaToolAnnotation]
	if toolName == "" {
		return nil
	}
	if toolName == dynamicSchemaTool {
		if len(positional) != 1 {
			return nil
		}
		toolName = positional[0]
	}
	tool, ok := schema.ToolByName(talento.Snapshot, toolName)
	if !ok {
		tool = schema.Tool{Name: "raw_call", InputSchema: schema.JSONSchema{Properties: map[string]schema.Property{}}}
	}
	rawJSON, _ := command.Flags().GetString("input")
	inputFile, _ := command.Flags().GetString("input-file")
	arguments, err := (&schemaInput{rawJSON: rawJSON, inputFile: inputFile}).arguments(command, tool)
	if err != nil {
		return err
	}
	commandContext := command.Context()
	if commandContext == nil {
		commandContext = context.Background()
	}
	command.SetContext(context.WithValue(commandContext, validatedInputKey{}, validatedInput{tool: tool.Name, arguments: arguments}))
	return nil
}

func validatedArguments(command *cobra.Command, input *schemaInput, tool schema.Tool) (map[string]any, error) {
	if commandContext := command.Context(); commandContext != nil {
		if cached, ok := commandContext.Value(validatedInputKey{}).(validatedInput); ok && cached.tool == tool.Name {
			return cached.arguments, nil
		}
	}
	return input.arguments(command, tool)
}

func (i *schemaInput) sourceArguments(command *cobra.Command) (map[string]any, error) {
	rawChanged := command.Flags().Changed("input")
	fileChanged := command.Flags().Changed("input-file")
	if rawChanged && fileChanged {
		return nil, inputUsage("--input and --input-file are mutually exclusive", "Choose one JSON input source; schema-derived flags may be combined with either source.")
	}
	if rawChanged {
		if len(i.rawJSON) > maxInputBytes {
			return nil, inputUsage("--input exceeds the 8 MiB limit", "Use a smaller JSON object.")
		}
		return decodeJSONObject(strings.NewReader(i.rawJSON), "--input")
	}
	if fileChanged {
		if i.inputFile == "-" {
			return decodeJSONObject(command.InOrStdin(), "--input-file -")
		}
		file, err := os.Open(i.inputFile)
		if err != nil {
			return nil, inputUsage("cannot read --input-file", err.Error())
		}
		defer file.Close()
		return decodeJSONObject(file, "--input-file")
	}
	return make(map[string]any), nil
}

func decodeJSONObject(reader io.Reader, source string) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return nil, inputUsage("cannot read "+source, "Provide a readable JSON object.")
	}
	if len(data) > maxInputBytes {
		return nil, inputUsage(source+" exceeds the 8 MiB limit", "Use a smaller JSON object.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, inputUsage("cannot parse "+source+" as JSON", jsonSyntaxHint(err))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, inputUsage(source+" must contain exactly one JSON value", "Remove trailing JSON values or text.")
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, inputUsage(source+" must be a JSON object", `Use an object such as {"name":"Ana"}; arrays, scalars, and null are not accepted.`)
	}
	return object, nil
}

func jsonSyntaxHint(err error) string {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Sprintf("Fix the JSON syntax near byte %d.", syntax.Offset)
	}
	if errors.Is(err, io.EOF) {
		return "Provide one non-empty JSON object."
	}
	return "Provide one valid JSON object."
}

func parseSchemaFlag(command *cobra.Command, flagName, propertyType string) (any, error) {
	if propertyType == "boolean" {
		value, err := command.Flags().GetBool(flagName)
		if err != nil {
			return nil, inputUsage("cannot read --"+flagName, "Pass true or false.")
		}
		return value, nil
	}
	raw, err := command.Flags().GetString(flagName)
	if err != nil {
		return nil, inputUsage("cannot read --"+flagName, "Check this command's --help.")
	}
	if propertyType == "array" || propertyType == "object" {
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		if err := decoder.Decode(&value); err != nil {
			return nil, inputUsage(fmt.Sprintf("--%s must be a valid JSON %s", flagName, propertyType), jsonSyntaxHint(err))
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, inputUsage(fmt.Sprintf("--%s must contain exactly one JSON %s", flagName, propertyType), "Remove trailing JSON values or text.")
		}
		if propertyType == "array" {
			if _, ok := value.([]any); !ok {
				return nil, inputUsage("--"+flagName+" must be a JSON array", "Pass one complete JSON array (use [] for empty).")
			}
		} else if _, ok := value.(map[string]any); !ok {
			return nil, inputUsage("--"+flagName+" must be a JSON object", "Pass one complete JSON object.")
		}
		return value, nil
	}
	value, err := parseScalarFlag(raw, propertyType)
	if err != nil {
		return nil, inputUsage(fmt.Sprintf("--%s must be %s", flagName, indefiniteType(propertyType)), "Check this command's --help for the expected value.")
	}
	return value, nil
}

func parseScalarFlag(raw, propertyType string) (any, error) {
	switch propertyType {
	case "integer":
		return strconv.ParseInt(raw, 10, 64)
	case "number":
		return strconv.ParseFloat(raw, 64)
	case "boolean":
		return strconv.ParseBool(raw)
	default:
		return raw, nil
	}
}

func indefiniteType(propertyType string) string {
	if propertyType == "integer" {
		return "an integer"
	}
	return "a " + propertyType
}

func inputUsage(message, hint string) error {
	return clioutput.Usage(message, hint)
}
