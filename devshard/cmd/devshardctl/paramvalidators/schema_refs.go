package paramvalidators

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DereferenceLocalSchemaRefs returns a copy of schema with local JSON Pointer $refs
// inlined and definition containers stripped. JSON-Schema data keyword values are
// preserved as-is because callers use the result to validate the effective schema;
// each caller decides whether to forward the normalized copy or the original request.
func DereferenceLocalSchemaRefs(schema map[string]any, maxNodes int) (map[string]any, error) {
	state := dereferenceState{
		stack:    map[string]struct{}{},
		maxNodes: maxNodes,
	}
	resolved, err := state.dereferenceSchemaValue(schema, schema, true)
	if err != nil {
		return nil, err
	}
	obj, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: root reference must resolve to an object", ErrSchemaRef)
	}
	return obj, nil
}

type dereferenceState struct {
	stack          map[string]struct{}
	expandedNodes  int
	maxNodes       int
	expandingDepth int
}

func (s *dereferenceState) dereferenceSchemaValue(root any, value any, countSchemaNode bool) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if rawRef, exists := typed["$ref"]; exists {
			ref, ok := rawRef.(string)
			if !ok || strings.TrimSpace(ref) == "" {
				return nil, fmt.Errorf("%w: $ref must be a non-empty string", ErrSchemaRef)
			}
			if _, seen := s.stack[ref]; seen {
				return nil, fmt.Errorf("%w: recursive reference %q", ErrSchemaRef, ref)
			}
			target, err := resolveLocalJSONPointer(root, ref)
			if err != nil {
				return nil, err
			}
			s.stack[ref] = struct{}{}
			s.expandingDepth++
			resolvedTarget, err := s.dereferenceSchemaValue(root, target, countSchemaNode)
			s.expandingDepth--
			delete(s.stack, ref)
			if err != nil {
				return nil, err
			}
			return s.mergeRefSiblings(root, resolvedTarget, typed)
		}

		if countSchemaNode && s.expandingDepth > 0 {
			if err := s.countNode(); err != nil {
				return nil, err
			}
		}
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isDefinitionContainer(key) {
				continue
			}
			if _, isData := schemaDataKeys[key]; isData {
				out[key] = child
				continue
			}
			resolvedChild, err := s.dereferenceSchemaChild(root, key, child)
			if err != nil {
				return nil, err
			}
			out[key] = resolvedChild
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			resolvedChild, err := s.dereferenceSchemaValue(root, child, true)
			if err != nil {
				return nil, err
			}
			out[i] = resolvedChild
		}
		return out, nil
	default:
		return typed, nil
	}
}

func (s *dereferenceState) dereferenceSchemaChild(root any, key string, value any) (any, error) {
	typed, ok := value.(map[string]any)
	if !ok {
		return s.dereferenceSchemaValue(root, value, true)
	}
	if _, isChildMap := schemaChildMapKeys[key]; !isChildMap {
		return s.dereferenceSchemaValue(root, typed, true)
	}
	out := make(map[string]any, len(typed))
	for childKey, child := range typed {
		resolvedChild, err := s.dereferenceSchemaValue(root, child, true)
		if err != nil {
			return nil, err
		}
		out[childKey] = resolvedChild
	}
	return out, nil
}

func (s *dereferenceState) mergeRefSiblings(root any, resolvedTarget any, refNode map[string]any) (any, error) {
	outMap, hasSiblings := resolvedTarget.(map[string]any)
	if !hasSiblings {
		for key := range refNode {
			if key != "$ref" && !isDefinitionContainer(key) {
				hasSiblings = true
				break
			}
		}
		if hasSiblings {
			return nil, fmt.Errorf("%w: cannot merge siblings into non-object reference target", ErrSchemaRef)
		}
		return resolvedTarget, nil
	}

	out := cloneMap(outMap)
	for key, child := range refNode {
		if key == "$ref" || isDefinitionContainer(key) {
			continue
		}
		if _, isData := schemaDataKeys[key]; isData {
			out[key] = child
			continue
		}
		resolvedChild, err := s.dereferenceSchemaChild(root, key, child)
		if err != nil {
			return nil, err
		}
		out[key] = resolvedChild
	}
	return out, nil
}

func (s *dereferenceState) countNode() error {
	if s.maxNodes <= 0 {
		return nil
	}
	s.expandedNodes++
	if s.expandedNodes > s.maxNodes {
		return fmt.Errorf("%w: limit %d", ErrSchemaNodes, s.maxNodes)
	}
	return nil
}

func resolveLocalJSONPointer(root any, ref string) (any, error) {
	if ref == "#" {
		return root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("%w: only local JSON Pointer references are supported: %q", ErrSchemaRef, ref)
	}
	pointer, err := url.PathUnescape(strings.TrimPrefix(ref, "#/"))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid reference %q: %v", ErrSchemaRef, ref, err)
	}
	current := root
	for _, rawToken := range strings.Split(pointer, "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[token]
			if !ok {
				return nil, fmt.Errorf("%w: unresolved reference %q", ErrSchemaRef, ref)
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("%w: unresolved reference %q", ErrSchemaRef, ref)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("%w: unresolved reference %q", ErrSchemaRef, ref)
		}
	}
	return current, nil
}

func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func isDefinitionContainer(key string) bool {
	return key == "$defs" || key == "definitions"
}
