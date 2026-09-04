package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParamError describes a single failed parameter, so a client learns exactly
// which input was wrong without the server disclosing anything else.
type ParamError struct {
	Param   string `json:"param"`
	Message string `json:"message"`
}

// ValidationError collects every parameter problem in one response, rather than
// making a caller fix them one request at a time.
type ValidationError struct {
	Errors []ParamError
}

func (v *ValidationError) Error() string {
	parts := make([]string, 0, len(v.Errors))
	for _, e := range v.Errors {
		parts = append(parts, e.Param+": "+e.Message)
	}
	return "invalid parameters: " + strings.Join(parts, "; ")
}

// Fields renders the errors as a field map for the HTTP error envelope.
func (v *ValidationError) Fields() map[string]string {
	out := make(map[string]string, len(v.Errors))
	for _, e := range v.Errors {
		out[e.Param] = e.Message
	}
	return out
}

// RawValues carries the unparsed inputs for one request.
type RawValues struct {
	Path  map[string]string
	Query map[string][]string
	Body  map[string]any
}

// lookup finds a parameter's raw value and whether it was supplied.
func (r RawValues) lookup(p Param) (any, bool) {
	switch p.In {
	case InPath:
		v, ok := r.Path[p.Name]
		return v, ok
	case InBody:
		v, ok := r.Body[p.Name]
		return v, ok
	default:
		vs, ok := r.Query[p.Name]
		if !ok || len(vs) == 0 {
			return nil, false
		}
		// Repeated query parameters take the first value; the builder has no
		// array parameter type, so the rest would be ambiguous.
		return vs[0], true
	}
}

// BindParams validates the request's inputs and returns the arguments to bind,
// in the order the parameters were declared.
//
// The returned slice is passed straight to the driver as bound arguments, which
// is what keeps caller input out of the SQL text.
func BindParams(params []Param, raw RawValues) ([]any, error) {
	verr := &ValidationError{}
	args := make([]any, 0, len(params))

	for _, p := range params {
		value, supplied := raw.lookup(p)

		if !supplied || value == nil || value == "" {
			if p.Default != nil {
				converted, err := convert(p, p.Default)
				if err != nil {
					// A bad default is a configuration fault, not a client
					// error, but failing the request is still the safe result.
					verr.Errors = append(verr.Errors, ParamError{p.Name, "the configured default value is not valid"})
					continue
				}
				args = append(args, converted)
				continue
			}
			if p.Required {
				verr.Errors = append(verr.Errors, ParamError{p.Name, "this parameter is required"})
				continue
			}
			// An optional parameter with no value binds as NULL, which lets a
			// statement written with "(? IS NULL OR col = ?)" ignore it.
			args = append(args, nil)
			continue
		}

		converted, err := convert(p, value)
		if err != nil {
			verr.Errors = append(verr.Errors, ParamError{p.Name, err.Error()})
			continue
		}
		if err := validateConstraints(p, converted); err != nil {
			verr.Errors = append(verr.Errors, ParamError{p.Name, err.Error()})
			continue
		}
		args = append(args, converted)
	}

	if len(verr.Errors) > 0 {
		return nil, verr
	}
	return args, nil
}

// convert coerces a raw value to the parameter's declared type.
func convert(p Param, value any) (any, error) {
	switch p.Type {
	case TypeInteger:
		switch v := value.(type) {
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("must be a whole number")
			}
			return n, nil
		case float64:
			if v != float64(int64(v)) {
				return nil, fmt.Errorf("must be a whole number")
			}
			return int64(v), nil
		case int64:
			return v, nil
		case int:
			return int64(v), nil
		case bool:
			return nil, fmt.Errorf("must be a whole number")
		default:
			return nil, fmt.Errorf("must be a whole number")
		}

	case TypeNumber:
		switch v := value.(type) {
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("must be a number")
			}
			return f, nil
		case float64:
			return v, nil
		case int64:
			return float64(v), nil
		case int:
			return float64(v), nil
		default:
			return nil, fmt.Errorf("must be a number")
		}

	case TypeBoolean:
		switch v := value.(type) {
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("must be true or false")
			}
			return b, nil
		case bool:
			return v, nil
		default:
			return nil, fmt.Errorf("must be true or false")
		}

	default:
		switch v := value.(type) {
		case string:
			return v, nil
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		case int64:
			return strconv.FormatInt(v, 10), nil
		case bool:
			return strconv.FormatBool(v), nil
		case nil:
			return nil, nil
		default:
			return nil, fmt.Errorf("must be text")
		}
	}
}

// validateConstraints applies the declared bounds to a converted value.
func validateConstraints(p Param, value any) error {
	switch v := value.(type) {
	case string:
		length := len([]rune(v))
		if p.MinLength != nil && length < *p.MinLength {
			return fmt.Errorf("must be at least %d characters", *p.MinLength)
		}
		if p.MaxLength != nil && length > *p.MaxLength {
			return fmt.Errorf("must be at most %d characters", *p.MaxLength)
		}
		if len(p.Enum) > 0 && !containsString(p.Enum, v) {
			return fmt.Errorf("must be one of: %s", strings.Join(p.Enum, ", "))
		}
		if p.Pattern != "" {
			// The pattern was compiled once at save time; a failure here means
			// stored configuration changed underneath us.
			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				return fmt.Errorf("the configured pattern is not valid")
			}
			if !re.MatchString(v) {
				return fmt.Errorf("does not match the required format")
			}
		}

	case int64:
		return checkRange(p, float64(v))
	case float64:
		return checkRange(p, v)
	}
	return nil
}

func checkRange(p Param, v float64) error {
	if p.Min != nil && v < *p.Min {
		return fmt.Errorf("must be at least %v", *p.Min)
	}
	if p.Max != nil && v > *p.Max {
		return fmt.Errorf("must be at most %v", *p.Max)
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
