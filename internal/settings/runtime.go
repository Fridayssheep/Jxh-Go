package settings

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
)

var ErrInvalidRuntime = errors.New("invalid settings runtime state")

type runtimeSnapshot struct {
	global        Features
	globalVersion uint64
	groups        map[string]RuntimeGroup
}

type Runtime struct {
	state atomic.Pointer[runtimeSnapshot]
}

func NewRuntime(initial Features, version uint64) (*Runtime, error) {
	if version == 0 || !validFeatures(initial) {
		return nil, ErrInvalidRuntime
	}
	runtime := &Runtime{}
	runtime.state.Store(&runtimeSnapshot{global: cloneFeatures(initial), globalVersion: version, groups: map[string]RuntimeGroup{}})
	return runtime, nil
}

func NewDefaultRuntime() *Runtime {
	runtime := &Runtime{}
	runtime.state.Store(&runtimeSnapshot{global: DefaultFeatures(), globalVersion: 1, groups: map[string]RuntimeGroup{}})
	return runtime
}

func (r *Runtime) Effective(groupID string) Features {
	state := r.load()
	if group, ok := state.groups[groupID]; ok {
		return resolve(state.global, group.Overrides)
	}
	return cloneFeatures(state.global)
}

func (r *Runtime) EffectiveForGroup(groupID int64) Features {
	return r.Effective(strconv.FormatInt(groupID, 10))
}

func (r *Runtime) Enabled(groupID int64, key FeatureKey) bool {
	features := r.EffectiveForGroup(groupID)
	switch key {
	case FeatureKeywordReply:
		return features.KeywordReply.Enabled
	case FeatureAIQA:
		return features.AIQA.Enabled
	case FeatureQuote:
		return features.Quote.Enabled
	case FeatureLinkCleaner:
		return features.LinkCleaner.Enabled
	case FeatureWelcome:
		return features.Welcome.Enabled
	case FeatureCustomCommand:
		return features.CustomCommand.Enabled
	default:
		return false
	}
}

func (r *Runtime) Replace(value RuntimeState) error {
	if !validGlobal(value.Global) {
		return ErrInvalidRuntime
	}
	groups := make(map[string]RuntimeGroup, len(value.Groups))
	for _, group := range value.Groups {
		if !validGroupID(group.GroupID) || group.Version == 0 || overridesEmpty(group.Overrides) || !validOverrides(group.Overrides) {
			return ErrInvalidRuntime
		}
		if _, duplicate := groups[group.GroupID]; duplicate {
			return ErrInvalidRuntime
		}
		groups[group.GroupID] = RuntimeGroup{GroupID: group.GroupID, Overrides: cloneOverrides(group.Overrides), Version: group.Version}
	}
	r.state.Store(&runtimeSnapshot{
		global: cloneFeatures(value.Global.Features), globalVersion: value.Global.Version, groups: groups,
	})
	return nil
}

func (r *Runtime) ApplyGlobal(value Global) error {
	if !validGlobal(value) {
		return ErrInvalidRuntime
	}
	for {
		current := r.load()
		if value.Version <= current.globalVersion {
			return ErrInvalidRuntime
		}
		next := &runtimeSnapshot{
			global: cloneFeatures(value.Features), globalVersion: value.Version, groups: cloneRuntimeGroups(current.groups),
		}
		if r.state.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func (r *Runtime) ApplyGroup(value Group) error {
	if !validGroup(value) {
		return ErrInvalidRuntime
	}
	for {
		current := r.load()
		if value.GlobalVersion != current.globalVersion || resolve(current.global, value.Overrides) != value.Effective {
			return ErrInvalidRuntime
		}
		next := &runtimeSnapshot{
			global: cloneFeatures(current.global), globalVersion: current.globalVersion, groups: cloneRuntimeGroups(current.groups),
		}
		if value.Version == 0 {
			delete(next.groups, value.GroupID)
		} else {
			if prior, ok := next.groups[value.GroupID]; ok && value.Version <= prior.Version {
				return ErrInvalidRuntime
			}
			next.groups[value.GroupID] = RuntimeGroup{
				GroupID: value.GroupID, Overrides: cloneOverrides(value.Overrides), Version: value.Version,
			}
		}
		if r.state.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func (r *Runtime) DeleteGroup(groupID string, expectedVersion uint64) error {
	if !validGroupID(groupID) || expectedVersion == 0 {
		return ErrInvalidRuntime
	}
	for {
		current := r.load()
		prior, exists := current.groups[groupID]
		if !exists || prior.Version != expectedVersion {
			return ErrInvalidRuntime
		}
		next := &runtimeSnapshot{
			global: cloneFeatures(current.global), globalVersion: current.globalVersion, groups: cloneRuntimeGroups(current.groups),
		}
		delete(next.groups, groupID)
		if r.state.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func RenderWelcome(template string, groupID, memberID int64) string {
	result := strings.ReplaceAll(template, "{{group_id}}", strconv.FormatInt(groupID, 10))
	return strings.ReplaceAll(result, "{{member_qq}}", strconv.FormatInt(memberID, 10))
}

func (r *Runtime) load() *runtimeSnapshot {
	if state := r.state.Load(); state != nil {
		return state
	}
	initial := &runtimeSnapshot{global: DefaultFeatures(), globalVersion: 1, groups: map[string]RuntimeGroup{}}
	if r.state.CompareAndSwap(nil, initial) {
		return initial
	}
	return r.state.Load()
}

func resolve(global Features, overrides Overrides) Features {
	result := cloneFeatures(global)
	if overrides.KeywordReply != nil {
		result.KeywordReply.Enabled = overrides.KeywordReply.Enabled
	}
	if overrides.AIQA != nil {
		result.AIQA.Enabled = overrides.AIQA.Enabled
	}
	if overrides.Quote != nil {
		result.Quote.Enabled = overrides.Quote.Enabled
	}
	if overrides.LinkCleaner != nil {
		result.LinkCleaner.Enabled = overrides.LinkCleaner.Enabled
	}
	if overrides.Welcome != nil {
		if overrides.Welcome.Enabled != nil {
			result.Welcome.Enabled = *overrides.Welcome.Enabled
		}
		if overrides.Welcome.MessageTemplate != nil {
			result.Welcome.MessageTemplate = *overrides.Welcome.MessageTemplate
		}
	}
	if overrides.CustomCommand != nil {
		result.CustomCommand.Enabled = overrides.CustomCommand.Enabled
	}
	return result
}

func cloneRuntimeGroups(values map[string]RuntimeGroup) map[string]RuntimeGroup {
	result := make(map[string]RuntimeGroup, len(values))
	for key, value := range values {
		result[key] = RuntimeGroup{GroupID: value.GroupID, Overrides: cloneOverrides(value.Overrides), Version: value.Version}
	}
	return result
}
