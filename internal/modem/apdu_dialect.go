package modem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrAPDUCommandRejected = errors.New("modem: APDU command rejected")
	ErrAPDUExecutionFailed = errors.New("modem: APDU execution failed")
	ErrAPDUResponseInvalid = errors.New("modem: APDU response is invalid")
)

type APDUCommandRejectedError struct {
	Command APDUCommand
	Final   string
}

func (err *APDUCommandRejectedError) Error() string {
	return fmt.Sprintf("modem: %s APDU command rejected: %s", apduCommandName(err.Command), err.Final)
}

func (err *APDUCommandRejectedError) Unwrap() error { return ErrAPDUCommandRejected }

// ATAPDUDialect learns CSIM and CGLA quoting independently. The zero value
// starts with the standard quoted form and can be used concurrently.
type ATAPDUDialect struct {
	mu      sync.Mutex
	forms   [2]APDUForm
	learned [2]bool
}

func (dialect *ATAPDUDialect) Execute(
	ctx context.Context,
	profile APDUCommandProfile,
	command APDUCommand,
	channel int,
	apdu []byte,
	execute func(context.Context, string) (Response, error),
) ([]byte, error) {
	if dialect == nil || profile == nil || execute == nil {
		return nil, ErrAPDUExecutionFailed
	}
	index, ok := apduCommandIndex(command)
	if !ok {
		return nil, ErrAPDUExecutionFailed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	dialect.mu.Lock()
	defer dialect.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, sanitizeAPDUExecutionError(err)
	}
	form := APDUQuoted
	if dialect.learned[index] {
		form = dialect.forms[index]
	} else if preference, ok := profile.(APDUFormPreference); ok {
		if preferred := preference.PreferredForm(command); preferred == APDUQuoted || preferred == APDUUnquoted {
			form = preferred
		}
	}
	result, rejected, err := executeAPDUForm(ctx, profile, command, channel, apdu, form, execute)
	if err != nil {
		return nil, err
	}
	if rejected == nil {
		dialect.learned[index] = true
		dialect.forms[index] = form
		return result, nil
	}
	if dialect.learned[index] {
		return nil, rejected
	}
	if err := ctx.Err(); err != nil {
		return nil, sanitizeAPDUExecutionError(err)
	}

	otherForm := APDUUnquoted
	if form == APDUUnquoted {
		otherForm = APDUQuoted
	}
	result, rejected, err = executeAPDUForm(ctx, profile, command, channel, apdu, otherForm, execute)
	if err != nil {
		return nil, err
	}
	if rejected != nil {
		return nil, rejected
	}
	dialect.learned[index] = true
	dialect.forms[index] = otherForm
	return result, nil
}

func executeAPDUForm(
	ctx context.Context,
	profile APDUCommandProfile,
	command APDUCommand,
	channel int,
	apdu []byte,
	form APDUForm,
	execute func(context.Context, string) (Response, error),
) ([]byte, *APDUCommandRejectedError, error) {
	commandText, err := profile.Build(command, channel, apdu, form)
	if err != nil {
		return nil, nil, ErrAPDUExecutionFailed
	}
	response, err := execute(ctx, commandText)
	if err != nil {
		if isUncertainAPDUExecutionError(err) {
			return nil, nil, sanitizeAPDUExecutionError(err)
		}
		var commandErr *CommandError
		if errors.As(err, &commandErr) {
			if final := normalizedATRejection(commandErr.Final); final != "" {
				rejected := newAPDUCommandRejectedError(command, final)
				if canRetryAPDUQuoting(final, response.Lines, commandErr.Lines) {
					return nil, rejected, nil
				}
				return nil, nil, rejected
			}
		}
		return nil, nil, sanitizeAPDUExecutionError(err)
	}
	if final := normalizedATRejection(response.Final); final != "" {
		rejected := newAPDUCommandRejectedError(command, final)
		if canRetryAPDUQuoting(final, response.Lines) {
			return nil, rejected, nil
		}
		return nil, nil, rejected
	}
	if !response.OK() {
		return nil, nil, ErrAPDUExecutionFailed
	}
	result, err := profile.Parse(command, response)
	if err != nil {
		return nil, nil, ErrAPDUResponseInvalid
	}
	return result, nil, nil
}

func isUncertainAPDUExecutionError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrCommandTimeout) ||
		errors.Is(err, ErrSessionClosed) ||
		errors.Is(err, ErrATPortBusy)
}

func newAPDUCommandRejectedError(command APDUCommand, final string) *APDUCommandRejectedError {
	return &APDUCommandRejectedError{Command: command, Final: final}
}

// A CME failure is not proof that the modem rejected the quoting syntax:
// phone/SIM failures can occur after a stateful APDU reached the card. Only
// a plain ERROR without an APDU response permits trying the other form.
func canRetryAPDUQuoting(final string, lineSets ...[]string) bool {
	if final != "ERROR" {
		return false
	}
	for _, lines := range lineSets {
		for _, line := range lines {
			line = strings.ToUpper(strings.TrimSpace(line))
			if strings.HasPrefix(line, "+CSIM:") || strings.HasPrefix(line, "+CGLA:") {
				return false
			}
		}
	}
	return true
}

func normalizedATRejection(final string) string {
	final = strings.TrimSpace(final)
	if strings.EqualFold(final, "ERROR") {
		return "ERROR"
	}
	const cmePrefix = "+CME ERROR:"
	if len(final) < len(cmePrefix) || !strings.EqualFold(final[:len(cmePrefix)], cmePrefix) {
		return ""
	}
	code := strings.TrimSpace(final[len(cmePrefix):])
	if !isDecimal(code) {
		return ""
	}
	return cmePrefix + " " + code
}

func sanitizeAPDUExecutionError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrCommandTimeout):
		return ErrCommandTimeout
	case errors.Is(err, ErrSessionClosed):
		return ErrSessionClosed
	case errors.Is(err, ErrATPortBusy):
		return ErrATPortBusy
	default:
		return ErrAPDUExecutionFailed
	}
}

func apduCommandIndex(command APDUCommand) (int, bool) {
	switch command {
	case APDUCSIM:
		return 0, true
	case APDUCGLA:
		return 1, true
	default:
		return 0, false
	}
}

func apduCommandName(command APDUCommand) string {
	switch command {
	case APDUCSIM:
		return "CSIM"
	case APDUCGLA:
		return "CGLA"
	default:
		return "unknown"
	}
}
