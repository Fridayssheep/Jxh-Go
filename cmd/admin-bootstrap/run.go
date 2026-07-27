package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/auth"
	"golang.org/x/term"
)

var (
	ErrAdminAlreadyExists        = errors.New("an admin user already exists")
	ErrPasswordInputNotTerminal  = errors.New("password input is not a terminal; use -password-stdin explicitly")
	ErrInvalidUsername           = errors.New("invalid admin username")
	ErrInvalidDisplayName        = errors.New("invalid admin display name")
	ErrInvalidPassword           = errors.New("invalid admin password")
	ErrBootstrapStoreUnavailable = errors.New("admin bootstrap store is unavailable")
	ErrBootstrapStoreFailure     = errors.New("admin bootstrap database operation failed")
)

const (
	minPasswordCharacters = 12
	maxPasswordCharacters = 128
	maxDisplayCharacters  = 64
	maxBootstrapIDBytes   = 64
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

type bootstrapAdmin = auth.BootstrapAdmin
type bootstrapStore = auth.BootstrapStore

type bootstrapDeps struct {
	hashPassword         func([]byte) (string, error)
	isTerminal           func(int) bool
	readTerminalPassword func(int) ([]byte, error)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, store bootstrapStore) error {
	hasher := auth.NewPasswordHasher(auth.DefaultPasswordParams(), rand.Reader)
	return runWithDeps(ctx, args, stdin, stdout, store, bootstrapDeps{
		hashPassword:         hasher.Hash,
		isTerminal:           term.IsTerminal,
		readTerminalPassword: term.ReadPassword,
	})
}

func runWithDeps(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	store bootstrapStore,
	deps bootstrapDeps,
) error {
	if stdin == nil || stdout == nil || deps.hashPassword == nil || deps.isTerminal == nil || deps.readTerminalPassword == nil {
		return errors.New("admin bootstrap command is not configured")
	}

	flags := flag.NewFlagSet("admin-bootstrap", flag.ContinueOnError)
	flags.SetOutput(stdout)
	flags.String("config", "config.yaml", "path to config file")
	username := flags.String("username", "", "username for the first super administrator")
	displayName := flags.String("display-name", "", "display name for the first super administrator")
	passwordStdin := flags.Bool("password-stdin", false, "read the password from stdin")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse bootstrap flags: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("parse bootstrap flags: unexpected positional arguments")
	}
	if !usernamePattern.MatchString(*username) {
		return ErrInvalidUsername
	}
	if !validDisplayName(*displayName) {
		return ErrInvalidDisplayName
	}
	if store == nil {
		return ErrBootstrapStoreUnavailable
	}

	password, err := readBootstrapPassword(stdin, *passwordStdin, deps)
	if err != nil {
		return err
	}
	defer clearBytes(password)
	if !validPassword(password) {
		return ErrInvalidPassword
	}
	passwordHash, err := deps.hashPassword(password)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("hash bootstrap password: %w", err)
		}
		return errors.New("hash bootstrap password: password hashing failed")
	}
	admin := bootstrapAdmin{
		User: auth.User{
			Username:    *username,
			DisplayName: *displayName,
			Role:        auth.RoleSuperAdmin,
			Enabled:     true,
			Version:     1,
		},
		PasswordHash: passwordHash,
	}
	created, ok, err := store.CreateFirstSuperAdmin(ctx, admin)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("create first super administrator: %w", err)
		}
		return ErrBootstrapStoreFailure
	}
	if !ok {
		return ErrAdminAlreadyExists
	}
	if !validCreatedAdmin(created, admin.User) {
		return errors.New("create first super administrator: store returned an invalid user")
	}
	if _, err := fmt.Fprintf(stdout, "user_id=%s username=%s\n", created.ID, created.Username); err != nil {
		return errors.New("write bootstrap result: output unavailable")
	}
	return nil
}

func readBootstrapPassword(stdin io.Reader, passwordStdin bool, deps bootstrapDeps) ([]byte, error) {
	if passwordStdin {
		return readPasswordLine(stdin)
	}
	terminalInput, ok := stdin.(interface{ Fd() uintptr })
	if !ok {
		return nil, ErrPasswordInputNotTerminal
	}
	fd := int(terminalInput.Fd())
	if !deps.isTerminal(fd) {
		return nil, ErrPasswordInputNotTerminal
	}
	password, err := deps.readTerminalPassword(fd)
	if err != nil {
		clearBytes(password)
		return nil, errors.New("read password from terminal: terminal input failed")
	}
	return password, nil
}

func readPasswordLine(input io.Reader) ([]byte, error) {
	const readLimit = maxPasswordCharacters*utf8.UTFMax + 3
	password, err := io.ReadAll(io.LimitReader(input, readLimit))
	if err != nil {
		clearBytes(password)
		return nil, errors.New("read password from stdin: input failed")
	}
	if len(password) == readLimit {
		clearBytes(password)
		return nil, ErrInvalidPassword
	}
	password = bytes.TrimSuffix(password, []byte{'\n'})
	password = bytes.TrimSuffix(password, []byte{'\r'})
	if bytes.ContainsAny(password, "\r\n") {
		clearBytes(password)
		return nil, ErrInvalidPassword
	}
	return password, nil
}

func validDisplayName(displayName string) bool {
	return utf8.ValidString(displayName) &&
		strings.TrimSpace(displayName) != "" &&
		utf8.RuneCountInString(displayName) <= maxDisplayCharacters
}

func validPassword(password []byte) bool {
	if !utf8.Valid(password) {
		return false
	}
	characters := utf8.RuneCount(password)
	return characters >= minPasswordCharacters && characters <= maxPasswordCharacters
}

func validCreatedAdmin(created, requested auth.User) bool {
	return created.ID != "" && len(created.ID) <= maxBootstrapIDBytes && utf8.ValidString(created.ID) &&
		!strings.ContainsAny(created.ID, "\x00\r\n\t") &&
		created.Username == requested.Username && created.DisplayName == requested.DisplayName &&
		created.Role == auth.RoleSuperAdmin && created.Enabled && created.Version == 1
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
