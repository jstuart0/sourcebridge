package graphql

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

type routeEntry struct {
	Method   string
	Path     string
	Handler  string
	FilePath string
	Line     int
	Symbol   *graphstore.StoredSymbol
}

var routePattern = regexp.MustCompile(`(?m)\br\.(Get|Post|Put|Patch|Delete)\("([^"]+)",\s*s\.(\w+)\)`)
var sourceCallPattern = regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func (r *queryResolver) executionEntryPoints(ctx context.Context, repositoryID string) ([]*ExecutionEntryPoint, error) {
	store := r.getStore(ctx)
	if store == nil {
		return []*ExecutionEntryPoint{}, nil
	}
	repo := store.GetRepository(repositoryID)
	if repo == nil {
		return []*ExecutionEntryPoint{}, nil
	}
	repoRoot, err := resolveRepoSourcePath(repo)
	if err != nil {
		return []*ExecutionEntryPoint{}, nil
	}
	routes, err := extractRouteEntryPoints(store, repositoryID, repoRoot)
	if err != nil {
		return nil, err
	}
	result := make([]*ExecutionEntryPoint, 0, len(routes))
	for _, route := range routes {
		lineStart := route.Line
		lineEnd := route.Line
		var symbolID *string
		var summary *string
		if route.Symbol != nil {
			symbolID = &route.Symbol.ID
			text := fmt.Sprintf("%s in %s", route.Handler, route.Symbol.FilePath)
			summary = &text
		}
		filePath := route.FilePath
		result = append(result, &ExecutionEntryPoint{
			Kind:      ExecutionEntryKindRoute,
			Label:     fmt.Sprintf("%s %s", route.Method, route.Path),
			Value:     fmt.Sprintf("%s %s", route.Method, route.Path),
			FilePath:  &filePath,
			LineStart: &lineStart,
			LineEnd:   &lineEnd,
			SymbolID:  symbolID,
			Summary:   summary,
		})
	}
	return result, nil
}

func (r *queryResolver) executionPath(ctx context.Context, input ExecutionPathInput) (*ExecutionPathResult, error) {
	store := r.getStore(ctx)
	if store == nil {
		return &ExecutionPathResult{EntryKind: input.EntryKind, EntryLabel: input.EntryValue, TrustQualified: false, Steps: []*ExecutionPathStep{}}, nil
	}
	repo := store.GetRepository(input.RepositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found", input.RepositoryID)
	}
	maxDepth := 6
	if input.MaxDepth != nil && *input.MaxDepth > 0 {
		maxDepth = *input.MaxDepth
	}

	var (
		entryLabel string
		steps      []*ExecutionPathStep
	)

	switch input.EntryKind {
	case ExecutionEntryKindRoute:
		repoRoot, err := resolveRepoSourcePath(repo)
		if err != nil {
			return trustedExecutionFallback(input.EntryKind, input.EntryValue, "This path is not well enough understood yet."), nil
		}
		routes, err := extractRouteEntryPoints(store, input.RepositoryID, repoRoot)
		if err != nil {
			return nil, err
		}
		var selected *routeEntry
		for i := range routes {
			if fmt.Sprintf("%s %s", routes[i].Method, routes[i].Path) == input.EntryValue {
				selected = &routes[i]
				break
			}
		}
		if selected == nil {
			return trustedExecutionFallback(input.EntryKind, input.EntryValue, "This path is not well enough understood yet."), nil
		}
		entryLabel = fmt.Sprintf("%s %s", selected.Method, selected.Path)
		filePath := selected.FilePath
		lineStart := selected.Line
		lineEnd := selected.Line
		steps = append(steps, &ExecutionPathStep{
			OrderIndex:  0,
			Kind:        "route",
			Label:       entryLabel,
			Explanation: "This HTTP route is the visible entry point into the backend flow.",
			Confidence:  "high",
			Observed:    true,
			Reason:      strPtr("Observed in route registration code"),
			FilePath:    &filePath,
			LineStart:   &lineStart,
			LineEnd:     &lineEnd,
		})
		if selected.Symbol != nil {
			steps = append(steps, executionStepsFromSymbolPath(store, input.RepositoryID, repoRoot, selected.Symbol.ID, maxDepth)...)
		}
	case ExecutionEntryKindSymbol:
		symbol := resolveSymbolEntry(store, input.RepositoryID, input.EntryValue)
		if symbol == nil {
			return trustedExecutionFallback(input.EntryKind, input.EntryValue, "This path is not well enough understood yet."), nil
		}
		entryLabel = symbol.Name
		repoRoot, err := resolveRepoSourcePath(repo)
		if err == nil {
			steps = executionStepsFromSymbolPath(store, input.RepositoryID, repoRoot, symbol.ID, maxDepth)
		} else {
			steps = executionStepsFromSymbolPath(store, input.RepositoryID, "", symbol.ID, maxDepth)
		}
	case ExecutionEntryKindFile:
		filePath := input.EntryValue
		entryLabel = filePath
		lineStart := 1
		steps = append(steps, &ExecutionPathStep{
			OrderIndex:  0,
			Kind:        "file",
			Label:       filePath,
			Explanation: "This file is the selected starting surface. The path follows the most likely code step inside it.",
			Confidence:  "medium",
			Observed:    true,
			Reason:      strPtr("Selected file scope"),
			FilePath:    &filePath,
			LineStart:   &lineStart,
		})
		symbol := resolveFileAnchorSymbol(store, input.RepositoryID, input.EntryValue)
		if symbol != nil {
			repoRoot, err := resolveRepoSourcePath(repo)
			if err == nil {
				steps = append(steps, executionStepsFromSymbolPath(store, input.RepositoryID, repoRoot, symbol.ID, maxDepth)...)
			} else {
				steps = append(steps, executionStepsFromSymbolPath(store, input.RepositoryID, "", symbol.ID, maxDepth)...)
			}
		}
	default:
		entryLabel = input.EntryValue
	}

	steps = append(steps, inferredWorkerHandoffSteps(store, input.RepositoryID, steps)...)
	result := buildExecutionPathResult(input.EntryKind, entryLabel, steps)
	if !result.TrustQualified {
		return trustedExecutionFallback(input.EntryKind, entryLabel, "This path is not well enough understood yet."), nil
	}
	return result, nil
}

func executionStepsFromSymbolPath(store graphstore.GraphStore, repoID, repoRoot, symbolID string, maxDepth int) []*ExecutionPathStep {
	nodes := graphstore.TraceLikelyExecutionPath(store, repoID, symbolID, maxDepth)
	symbol := store.GetSymbol(symbolID)
	if symbol != nil {
		nodes = mergeExecutionNodes(nodes, inferSourceHelperNodes(store, repoID, repoRoot, symbol, maxDepth))
	}
	steps := make([]*ExecutionPathStep, 0, len(nodes))
	for i, node := range nodes {
		filePath := node.FilePath
		lineStart := node.StartLine
		lineEnd := node.EndLine
		symbolID := node.SymbolID
		symbolName := node.SymbolName
		explanation := "This code step participates in the likely backend flow."
		if node.Reason == "Selected code step" {
			explanation = "This is the focused code step the path is anchored on."
		} else if node.Reason == "Calls the next step" {
			explanation = "This step appears to call into the next code step in the flow."
		} else if node.Reason == "Called by the previous step" {
			explanation = "This step appears downstream of the previous code step."
		} else if node.Reason == "Observed in same-file source call" {
			explanation = "This helper is called directly inside the focused code step."
		}
		steps = append(steps, &ExecutionPathStep{
			OrderIndex:  i,
			Kind:        "code",
			Label:       node.SymbolName,
			Explanation: explanation,
			Confidence:  "medium",
			Observed:    node.Observed,
			Reason:      &node.Reason,
			FilePath:    &filePath,
			LineStart:   &lineStart,
			LineEnd:     &lineEnd,
			SymbolID:    &symbolID,
			SymbolName:  &symbolName,
		})
	}
	return steps
}

func mergeExecutionNodes(base []graphstore.ExecutionNode, extras []graphstore.ExecutionNode) []graphstore.ExecutionNode {
	if len(extras) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	merged := make([]graphstore.ExecutionNode, 0, len(base)+len(extras))
	for _, node := range base {
		merged = append(merged, node)
		if node.SymbolID != "" {
			seen[node.SymbolID] = true
		}
	}
	for _, node := range extras {
		if node.SymbolID != "" && seen[node.SymbolID] {
			continue
		}
		merged = append(merged, node)
		if node.SymbolID != "" {
			seen[node.SymbolID] = true
		}
	}
	return merged
}

func inferSourceHelperNodes(store graphstore.GraphStore, repoID, repoRoot string, symbol *graphstore.StoredSymbol, maxDepth int) []graphstore.ExecutionNode {
	if symbol == nil || repoRoot == "" || maxDepth <= 0 {
		return nil
	}
	content, err := readSourceFile(repoRoot, symbol.FilePath)
	if err != nil {
		return nil
	}
	lines := strings.Split(content, "\n")
	start := symbol.StartLine - 1
	if start < 0 || start >= len(lines) {
		return nil
	}
	end := symbol.EndLine
	if end <= start || end > len(lines) {
		end = minInt(len(lines), start+120)
	}
	body := strings.Join(lines[start:end], "\n")
	sameFile := store.GetSymbolsByFile(repoID, symbol.FilePath)
	byName := make(map[string]*graphstore.StoredSymbol, len(sameFile))
	for _, candidate := range sameFile {
		if candidate == nil || candidate.ID == symbol.ID || candidate.IsTest {
			continue
		}
		if _, exists := byName[candidate.Name]; !exists {
			byName[candidate.Name] = candidate
		}
	}
	ignored := map[string]bool{
		"if": true, "for": true, "switch": true, "return": true, "make": true, "len": true,
		"append": true, "delete": true, "close": true, "copy": true, "new": true, "panic": true,
	}
	seen := map[string]bool{}
	results := make([]graphstore.ExecutionNode, 0, maxDepth)
	for _, match := range sourceCallPattern.FindAllStringSubmatch(body, -1) {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if ignored[name] || seen[name] {
			continue
		}
		candidate := byName[name]
		if candidate == nil {
			continue
		}
		seen[name] = true
		results = append(results, graphstore.ExecutionNode{
			SymbolID:   candidate.ID,
			SymbolName: candidate.Name,
			FilePath:   candidate.FilePath,
			StartLine:  candidate.StartLine,
			EndLine:    candidate.EndLine,
			Observed:   true,
			Reason:     "Observed in same-file source call",
		})
		if len(results) >= maxDepth {
			break
		}
	}
	return results
}

func buildExecutionPathResult(kind ExecutionEntryKind, entryLabel string, steps []*ExecutionPathStep) *ExecutionPathResult {
	result := &ExecutionPathResult{
		EntryKind:  kind,
		EntryLabel: entryLabel,
		Steps:      steps,
	}
	for i, step := range steps {
		step.OrderIndex = i
		if step.Observed {
			result.ObservedStepCount++
		} else {
			result.InferredStepCount++
		}
	}
	total := len(steps)
	if total > 0 {
		result.TrustQualified = result.ObservedStepCount >= 3 || float64(result.ObservedStepCount)/float64(total) >= 0.5
	}
	return result
}

func trustedExecutionFallback(kind ExecutionEntryKind, entryLabel string, message string) *ExecutionPathResult {
	return &ExecutionPathResult{
		EntryKind:         kind,
		EntryLabel:        entryLabel,
		Message:           &message,
		TrustQualified:    false,
		ObservedStepCount: 0,
		InferredStepCount: 0,
		Steps:             []*ExecutionPathStep{},
	}
}

func resolveSymbolEntry(store graphstore.GraphStore, repoID, entryValue string) *graphstore.StoredSymbol {
	if sym := store.GetSymbol(entryValue); sym != nil {
		return sym
	}
	if strings.Contains(entryValue, "#") {
		filePath, symbolName, _ := strings.Cut(entryValue, "#")
		for _, sym := range store.GetSymbolsByFile(repoID, filePath) {
			if sym.Name == symbolName || sym.QualifiedName == symbolName {
				return sym
			}
		}
	}
	return nil
}

func resolveFileAnchorSymbol(store graphstore.GraphStore, repoID, filePath string) *graphstore.StoredSymbol {
	symbols := store.GetSymbolsByFile(repoID, filePath)
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].StartLine == symbols[j].StartLine {
			return symbols[i].Name < symbols[j].Name
		}
		return symbols[i].StartLine < symbols[j].StartLine
	})
	for _, sym := range symbols {
		if !sym.IsTest {
			return sym
		}
	}
	if len(symbols) == 0 {
		return nil
	}
	return symbols[0]
}

func extractRouteEntryPoints(store graphstore.GraphStore, repoID, repoRoot string) ([]routeEntry, error) {
	allSymbols, _ := store.GetSymbols(repoID, nil, nil, 0, 0)
	symbolsByName := make(map[string]*graphstore.StoredSymbol, len(allSymbols))
	for _, sym := range allSymbols {
		if _, exists := symbolsByName[sym.Name]; !exists {
			symbolsByName[sym.Name] = sym
		}
	}

	seen := map[string]bool{}
	var routes []routeEntry
	for _, file := range store.GetFiles(repoID) {
		if file.Language != "go" {
			continue
		}
		content, err := readSourceFile(repoRoot, file.Path)
		if err != nil {
			continue
		}
		matches := routePattern.FindAllStringSubmatchIndex(content, -1)
		for _, match := range matches {
			method := content[match[2]:match[3]]
			path := content[match[4]:match[5]]
			handler := content[match[6]:match[7]]
			key := method + " " + path
			if seen[key] {
				continue
			}
			seen[key] = true
			line := 1 + strings.Count(content[:match[0]], "\n")
			routes = append(routes, routeEntry{
				Method:   method,
				Path:     path,
				Handler:  handler,
				FilePath: file.Path,
				Line:     line,
				Symbol:   symbolsByName[handler],
			})
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Method == routes[j].Method {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

func inferredWorkerHandoffSteps(store graphstore.GraphStore, repoID string, steps []*ExecutionPathStep) []*ExecutionPathStep {
	if len(steps) == 0 {
		return nil
	}
	last := steps[len(steps)-1]
	if last.FilePath == nil || *last.FilePath != "internal/worker/client.go" || last.SymbolName == nil {
		return nil
	}

	targetFile, explanation := inferredWorkerTarget(store, repoID, *last.SymbolName)
	if targetFile == "" {
		return []*ExecutionPathStep{{
			OrderIndex:  len(steps),
			Kind:        "handoff",
			Label:       "Worker handoff",
			Explanation: "This client call likely hands work to a background worker over gRPC.",
			Confidence:  "low",
			Observed:    false,
			Reason:      strPtr("Inferred from worker client boundary"),
		}}
	}

	return []*ExecutionPathStep{
		{
			OrderIndex:  len(steps),
			Kind:        "handoff",
			Label:       "Worker handoff",
			Explanation: "This client call likely hands work to a background worker over gRPC.",
			Confidence:  "low",
			Observed:    false,
			Reason:      strPtr("Inferred from worker client boundary"),
		},
		{
			OrderIndex:  len(steps) + 1,
			Kind:        "worker",
			Label:       targetFile,
			Explanation: explanation,
			Confidence:  "low",
			Observed:    false,
			Reason:      strPtr("Inferred cross-language worker target"),
			FilePath:    &targetFile,
		},
	}
}

func inferredWorkerTarget(store graphstore.GraphStore, repoID, symbolName string) (string, string) {
	targets := map[string]string{
		"GenerateCliffNotes":   "workers/knowledge/servicer.py",
		"GenerateLearningPath": "workers/knowledge/servicer.py",
		"GenerateCodeTour":     "workers/knowledge/servicer.py",
		"ExplainSystem":        "workers/knowledge/servicer.py",
		"ReviewFile":           "workers/reasoning/servicer.py",
		"AnswerQuestion":       "workers/reasoning/servicer.py",
		"AnalyzeSymbol":        "workers/reasoning/servicer.py",
		"LinkRequirement":      "workers/linking/servicer.py",
	}
	filePath := targets[symbolName]
	if filePath == "" {
		return "", ""
	}
	for _, file := range store.GetFiles(repoID) {
		if file.Path == filePath {
			return filePath, "This is the likely Python worker entry point for the handoff."
		}
	}
	return "", ""
}

func strPtr(value string) *string {
	return &value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
