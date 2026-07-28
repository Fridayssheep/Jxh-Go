package customcommand

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type parsedValue struct {
	raw      string
	display  string
	integer  int64
	duration time.Duration
	member   string
	present  bool
}

func parseMessage(definition Definition, message string) (map[string]parsedValue, []ParsedArgument, error) {
	if !validRuneText(message, 1, maxMessageRunes) {
		return nil, nil, fmt.Errorf("%w: invalid message", ErrInvalidInput)
	}
	tokens, err := tokenize(message)
	if err != nil || len(tokens) == 0 || tokens[0] != definition.Name {
		return nil, nil, fmt.Errorf("%w: command does not match", ErrInvalidInput)
	}
	if len(tokens)-1 > len(definition.Parameters) {
		return nil, nil, fmt.Errorf("%w: too many arguments", ErrInvalidInput)
	}
	values := make(map[string]parsedValue, len(definition.Parameters))
	parsed := make([]ParsedArgument, 0, len(definition.Parameters))
	for index, parameter := range definition.Parameters {
		if index+1 >= len(tokens) {
			if parameter.Required {
				return nil, nil, fmt.Errorf("%w: missing argument %s", ErrInvalidInput, parameter.Name)
			}
			values[parameter.Name] = parsedValue{}
			continue
		}
		value, err := parseValue(parameter, tokens[index+1])
		if err != nil {
			return nil, nil, err
		}
		values[parameter.Name] = value
		parsed = append(parsed, ParsedArgument{Name: parameter.Name, Type: parameter.Type, DisplayValue: value.display})
	}
	return values, parsed, nil
}

func parseValue(parameter Parameter, raw string) (parsedValue, error) {
	value := parsedValue{raw: raw, display: raw, present: true}
	switch parameter.Type {
	case ParameterText:
		length := utf8.RuneCountInString(raw)
		if !utf8.ValidString(raw) || length < parameter.MinLength || length > parameter.MaxLength {
			return parsedValue{}, fmt.Errorf("%w: text argument %s is outside its length limits", ErrInvalidInput, parameter.Name)
		}
	case ParameterInteger:
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < parameter.Minimum || parsed > parameter.Maximum {
			return parsedValue{}, fmt.Errorf("%w: integer argument %s is outside its limits", ErrInvalidInput, parameter.Name)
		}
		value.integer, value.raw, value.display = parsed, strconv.FormatInt(parsed, 10), strconv.FormatInt(parsed, 10)
	case ParameterDuration:
		seconds, err := parseDurationSeconds(raw)
		if err != nil || seconds < parameter.MinimumSeconds || seconds > parameter.MaximumSeconds {
			return parsedValue{}, fmt.Errorf("%w: duration argument %s is outside its limits", ErrInvalidInput, parameter.Name)
		}
		value.duration = time.Duration(seconds) * time.Second
		value.raw, value.display = strconv.FormatInt(seconds, 10), fmt.Sprintf("%ds", seconds)
	case ParameterMember:
		member := strings.TrimPrefix(raw, "@")
		if !validQQ(member) {
			return parsedValue{}, fmt.Errorf("%w: member argument %s is invalid", ErrInvalidInput, parameter.Name)
		}
		value.member, value.raw, value.display = member, member, member
	case ParameterFixedOption:
		found := false
		for _, option := range parameter.Options {
			if option.Value == raw {
				value.display = option.Label
				found = true
				break
			}
		}
		if !found {
			return parsedValue{}, fmt.Errorf("%w: fixed option argument %s is invalid", ErrInvalidInput, parameter.Name)
		}
	default:
		return parsedValue{}, ErrInvalidInput
	}
	return value, nil
}

func tokenize(value string) ([]string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\u3000", " ")
	var tokens []string
	var current strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if started {
			tokens = append(tokens, current.String())
			current.Reset()
			started = false
		}
	}
	for _, character := range value {
		if escaped {
			current.WriteRune(character)
			escaped = false
			started = true
			continue
		}
		if character == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			started = true
			continue
		}
		if unicode.IsSpace(character) {
			flush()
			continue
		}
		current.WriteRune(character)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quoted argument")
	}
	flush()
	return tokens, nil
}

func parseDurationSeconds(value string) (int64, error) {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return seconds, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		if err != nil || days <= 0 || days > maxMuteSeconds/86400 {
			return 0, ErrInvalidInput
		}
		return days * 86400, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 || duration%time.Second != 0 {
		return 0, ErrInvalidInput
	}
	return int64(duration / time.Second), nil
}

func validQQ(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return strings.TrimLeft(value, "0") != ""
}

func renderTemplate(template string, input ValidationSample, values map[string]parsedValue) string {
	return templateVariable.ReplaceAllStringFunc(template, func(match string) string {
		name := templateVariable.FindStringSubmatch(match)[1]
		switch name {
		case "sender_qq":
			return input.SenderQQ
		case "group_id":
			return input.GroupID
		default:
			return values[name].raw
		}
	})
}

func renderActions(definition Definition, sample ValidationSample, values map[string]parsedValue) []RenderedAction {
	result := make([]RenderedAction, 0, len(definition.Actions))
	for index, action := range definition.Actions {
		preview := ""
		switch action.Type {
		case ActionReplyText:
			preview = renderTemplate(action.Template, sample, values)
		case ActionMention:
			member := sample.SenderQQ
			if action.Target == MentionParameter {
				member = values[action.MemberParameter].member
			}
			preview = "@" + member
		case ActionMuteMember:
			seconds := action.Duration.Seconds
			if action.Duration.Type == DurationParameter {
				seconds = int64(values[action.Duration.Parameter].duration / time.Second)
			}
			preview = fmt.Sprintf("mute %s for %ds", values[action.MemberParameter].member, seconds)
		case ActionSendGroupText:
			preview = fmt.Sprintf("groups[%s]: %s", strings.Join(action.TargetGroupIDs, ","), renderTemplate(action.Template, sample, values))
		}
		if utf8.RuneCountInString(preview) > maxPreviewRunes {
			preview = string([]rune(preview)[:maxPreviewRunes])
		}
		result = append(result, RenderedAction{Index: index, Type: action.Type, Preview: preview})
	}
	return result
}
