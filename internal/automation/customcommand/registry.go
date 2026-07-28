package customcommand

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var defaultBuiltinNames = []string{"/test", "/reload", "/q", "/ai", "/admin"}

type registrySnapshot struct {
	byName   map[string]Command
	byID     map[string]string
	builtins map[string]struct{}
}

// Registry publishes whole immutable snapshots. Readers never take the write
// lock and receive deep copies of definitions.
type Registry struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[registrySnapshot]
}

func NewRegistry(builtinNames []string) (*Registry, error) {
	if builtinNames == nil {
		builtinNames = defaultBuiltinNames
	}
	builtins := make(map[string]struct{}, len(builtinNames))
	for _, name := range builtinNames {
		if !commandNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%w: invalid builtin command name", ErrInvalidInput)
		}
		builtins[name] = struct{}{}
	}
	registry := &Registry{}
	registry.snapshot.Store(&registrySnapshot{byName: map[string]Command{}, byID: map[string]string{}, builtins: builtins})
	return registry, nil
}

func (r *Registry) Replace(commands []Command) error {
	if r == nil {
		return ErrInvalidInput
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	next := &registrySnapshot{byName: make(map[string]Command), byID: make(map[string]string), builtins: cloneSet(current.builtins)}
	for _, source := range commands {
		command, err := compileStoredCommand(source)
		if err != nil {
			return err
		}
		if !command.Enabled || command.Status != StatusActive {
			continue
		}
		if _, builtin := next.builtins[command.Name]; builtin {
			return ErrConflict
		}
		if _, duplicate := next.byName[command.Name]; duplicate {
			return ErrConflict
		}
		next.byName[command.Name] = cloneCommand(command)
		next.byID[command.ID] = command.Name
	}
	r.snapshot.Store(next)
	return nil
}

func (r *Registry) Upsert(command Command) error {
	if r == nil {
		return ErrInvalidInput
	}
	command, err := compileStoredCommand(command)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	next := cloneSnapshot(current)
	if oldName, ok := next.byID[command.ID]; ok {
		delete(next.byName, oldName)
		delete(next.byID, command.ID)
	}
	if command.Enabled && command.Status == StatusActive {
		if _, builtin := next.builtins[command.Name]; builtin {
			return ErrConflict
		}
		if existing, duplicate := next.byName[command.Name]; duplicate && existing.ID != command.ID {
			return ErrConflict
		}
		next.byName[command.Name] = cloneCommand(command)
		next.byID[command.ID] = command.Name
	}
	r.snapshot.Store(next)
	return nil
}

func (r *Registry) Remove(commandID string) {
	if r == nil || commandID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	name, exists := current.byID[commandID]
	if !exists {
		return
	}
	next := cloneSnapshot(current)
	delete(next.byID, commandID)
	delete(next.byName, name)
	r.snapshot.Store(next)
}

func (r *Registry) Conflicts(name, excludingCommandID string) bool {
	if r == nil {
		return false
	}
	snapshot := r.load()
	if _, builtin := snapshot.builtins[name]; builtin {
		return true
	}
	command, exists := snapshot.byName[name]
	return exists && command.ID != excludingCommandID
}

func (r *Registry) Match(message, groupID string) (Command, bool) {
	if r == nil {
		return Command{}, false
	}
	fields := strings.Fields(strings.ReplaceAll(strings.TrimSpace(message), "\u3000", " "))
	if len(fields) == 0 {
		return Command{}, false
	}
	command, found := r.load().byName[fields[0]]
	if !found || !inScope(command.Scope, groupID) {
		return Command{}, false
	}
	return cloneCommand(command), true
}

func (r *Registry) load() *registrySnapshot {
	if r == nil {
		return &registrySnapshot{byName: map[string]Command{}, byID: map[string]string{}, builtins: map[string]struct{}{}}
	}
	if snapshot := r.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	initial := &registrySnapshot{byName: map[string]Command{}, byID: map[string]string{}, builtins: map[string]struct{}{}}
	if r.snapshot.CompareAndSwap(nil, initial) {
		return initial
	}
	return r.snapshot.Load()
}

func cloneSnapshot(source *registrySnapshot) *registrySnapshot {
	result := &registrySnapshot{
		byName: make(map[string]Command, len(source.byName)), byID: make(map[string]string, len(source.byID)),
		builtins: cloneSet(source.builtins),
	}
	for name, command := range source.byName {
		result.byName[name] = cloneCommand(command)
	}
	for id, name := range source.byID {
		result.byID[id] = name
	}
	return result
}

func cloneSet(source map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(source))
	for value := range source {
		result[value] = struct{}{}
	}
	return result
}

func inScope(scope Scope, groupID string) bool {
	if scope.Type == ScopeGlobal {
		return true
	}
	for _, candidate := range scope.GroupIDs {
		if candidate == groupID {
			return true
		}
	}
	return false
}

func compileStoredCommand(command Command) (Command, error) {
	if !validIdentifier(command.ID) || command.Version == 0 || !validStatus(command.Status) {
		return Command{}, ErrInvalidInput
	}
	compiled, err := Compile(command.Definition)
	if err != nil {
		return Command{}, err
	}
	command.Definition = compiled.Definition()
	if command.Enabled != (command.Status == StatusActive) {
		return Command{}, ErrInvalidInput
	}
	return cloneCommand(command), nil
}
