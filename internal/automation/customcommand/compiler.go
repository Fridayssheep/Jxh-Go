package customcommand

// Compiled is an immutable, validated command definition. Its contents are
// kept private so callers cannot mutate a registry definition after publish.
type Compiled struct {
	definition Definition
}

func Compile(definition Definition) (*Compiled, error) {
	if len(ValidateDefinition(definition)) != 0 {
		return nil, ErrInvalidInput
	}
	return &Compiled{definition: cloneDefinition(definition)}, nil
}

func (c *Compiled) Definition() Definition {
	if c == nil {
		return Definition{}
	}
	return cloneDefinition(c.definition)
}

func (c *Compiled) Parse(message string) ([]ParsedArgument, error) {
	if c == nil {
		return nil, ErrInvalidInput
	}
	_, parsed, err := parseMessage(c.definition, message)
	return append([]ParsedArgument(nil), parsed...), err
}
