// Package prompt implements the interactive half of login against a terminal.
//
// It satisfies enteapi.Prompter. Keeping it separate from the API client means
// the login flow has no opinion about where its input comes from, and nothing
// in this project reads os.Stdin behind a caller's back.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Terminal prompts on os.Stdin and reports to os.Stderr.
//
// Messages go to stderr, not stdout, so that command output stays pipeable:
// `ente-public-galleries list | column -t` should not have "Deriving key..."
// spliced into it.
type Terminal struct {
	in *bufio.Reader
}

// NewTerminal returns a Terminal reading from os.Stdin.
func NewTerminal() *Terminal {
	return &Terminal{in: bufio.NewReader(os.Stdin)}
}

// ErrNotATerminal is returned when a secret is requested but stdin is not a
// terminal, so there is no way to read it without echoing.
var ErrNotATerminal = errors.New("stdin is not a terminal, so a password cannot be read securely")

// Password reads a line without echoing it.
func (t *Terminal) Password(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", ErrNotATerminal
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	secret, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("%s was empty", label)
	}
	return string(secret), nil
}

// Code reads a short code, echoed, since there is no value in hiding a
// single-use token the user is reading off another screen.
func (t *Terminal) Code(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := t.readLine()
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	// Codes get pasted with stray spaces surprisingly often.
	line = strings.ReplaceAll(line, " ", "")
	if line == "" {
		return "", fmt.Errorf("%s was empty", label)
	}
	return line, nil
}

// Line reads a plain, echoed line of input.
func (t *Terminal) Line(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := t.readLine()
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	if line == "" {
		return "", fmt.Errorf("%s was empty", label)
	}
	return line, nil
}

// Notify reports progress or instructions.
func (t *Terminal) Notify(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// WaitForEnter blocks until the user presses Enter.
func (t *Terminal) WaitForEnter(promptText string) error {
	fmt.Fprintf(os.Stderr, "%s... ", promptText)
	if _, err := t.in.ReadString('\n'); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("waiting for confirmation: %w", err)
	}
	return nil
}

func (t *Terminal) readLine() (string, error) {
	line, err := t.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if line == "" && errors.Is(err, io.EOF) {
		return "", io.EOF
	}
	return strings.TrimSpace(line), nil
}
