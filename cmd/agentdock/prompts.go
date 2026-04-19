package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/term"
)

var (
	stderr  = os.Stderr
	scanner = bufio.NewScanner(os.Stdin)
)

// checkAgentCLI verifies that the named CLI binary is available and returns
// the first line of its --version output (stdout+stderr combined).
func checkAgentCLI(command string) (string, error) {
	cmd := exec.Command(command, "--version")

	out, err := cmd.CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return "", execErr
		}
		// Non-zero exit is fine — many CLIs exit non-zero for --version.
	}

	localScanner := bufio.NewScanner(bytes.NewReader(out))
	if localScanner.Scan() {
		return strings.TrimSpace(localScanner.Text()), nil
	}
	return "", nil
}

// printOK prints a success line to stderr.
func printOK(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[32m✓\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printFail prints a failure line to stderr.
func printFail(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[31m✗\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printWarn prints a warning line to stderr.
func printWarn(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[33m⚠\033[0m %s\n", fmt.Sprintf(format, args...))
}

// promptLine prints a prompt and reads a line of text input.
func promptLine(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// promptHidden prints a prompt and reads input without echo (for secrets).
func promptHidden(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(stderr) // newline after hidden input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// promptYesNo prints a yes/no prompt. Default is yes.
func promptYesNo(prompt string) bool {
	answer := promptLine(fmt.Sprintf("%s [Y/n]: ", prompt))
	return answer == "" || strings.ToLower(answer) == "y" || strings.ToLower(answer) == "yes"
}
