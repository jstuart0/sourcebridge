package graphql

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	"github.com/sourcebridge/sourcebridge/internal/architecture"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// clientTypeFromContext returns the client type for the current request.
// API tokens (VS Code, JetBrains) carry client_type; JWT sessions (web) do not.
func clientTypeFromContext(ctx context.Context) string {
	if tok := auth.GetAPIToken(ctx); tok != nil && tok.ClientType != "" {
		return tok.ClientType
	}
	return "web"
}

func isGitURL(path string) bool {
	return strings.HasPrefix(path, "http://") ||
		strings.HasPrefix(path, "https://") ||
		strings.HasPrefix(path, "git://") ||
		strings.HasPrefix(path, "git@") ||
		strings.HasSuffix(path, ".git")
}

// normalizeGitURL normalizes a git URL for deduplication.
// Strips trailing .git, normalizes SSH URLs to HTTPS form.
func normalizeGitURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	// Normalize git@github.com:user/repo → https://github.com/user/repo
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
		url = "https://" + url
	}
	url = strings.TrimSuffix(url, "/")
	return strings.ToLower(url)
}

// injectTokenIntoURL injects a personal access token into an HTTPS git URL.
// e.g., https://github.com/user/repo → https://x-access-token:{token}@github.com/user/repo
func injectTokenIntoURL(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	// Only inject into HTTPS URLs
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(rawURL, prefix) {
			return prefix + "x-access-token:" + token + "@" + rawURL[len(prefix):]
		}
	}
	return rawURL
}

// gitCloneCmd builds an exec.Cmd for git clone with optional authentication.
// For HTTPS repos, it injects the token into the URL.
// For SSH repos, it sets GIT_SSH_COMMAND to use a specific key if provided.
func gitCloneCmd(ctx context.Context, repoURL, targetDir, token, sshKeyPath string) *exec.Cmd {
	cloneURL := repoURL
	if token != "" && (strings.HasPrefix(repoURL, "https://") || strings.HasPrefix(repoURL, "http://")) {
		cloneURL = injectTokenIntoURL(repoURL, token)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", cloneURL, targetDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if sshKeyPath != "" && strings.HasPrefix(repoURL, "git@") {
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=accept-new", sshKeyPath))
	}
	return cmd
}

// gitPullCmd builds an exec.Cmd for git pull with optional authentication.
func gitPullCmd(ctx context.Context, repoDir, token, sshKeyPath string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "pull", "--ff-only")
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if token != "" {
		// Set credential helper to return the stored token.
		// Shell-quote the token to prevent injection via special characters.
		quoted := "'" + strings.ReplaceAll(token, "'", "'\\''") + "'"
		cmd.Env = append(os.Environ(),
			"GIT_ASKPASS=echo",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential.helper",
			fmt.Sprintf("GIT_CONFIG_VALUE_0=!f() { echo password=%s; }; f", quoted),
		)
	}
	if sshKeyPath != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=accept-new", sshKeyPath))
	}
	return cmd
}

// sanitizeRepoName creates a filesystem-safe name from a repository name.
func sanitizeRepoName(name string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return r.Replace(name)
}

// resolveRepoSourcePath returns the local filesystem path for a repository.
// For remote repos with a persisted clone, returns the clone path.
// Returns an error if no readable source is available on disk.
func resolveRepoSourcePath(repo *graphstore.Repository) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("repository is nil")
	}

	// For remote repos, prefer the persisted clone path
	if isGitURL(repo.Path) || repo.RemoteURL != "" {
		if repo.ClonePath != "" {
			info, err := os.Stat(repo.ClonePath)
			if err == nil && info.IsDir() {
				return repo.ClonePath, nil
			}
		}
		// Fallback: try computed cache path from repo name (matches IndexRepository logic)
		for _, cacheBase := range []string{"./repo-cache", "/data/repo-cache"} {
			computed := filepath.Join(cacheBase, "repos", sanitizeRepoName(repo.Name))
			if info, err := os.Stat(computed); err == nil && info.IsDir() {
				return computed, nil
			}
		}
		return "", fmt.Errorf("repository source unavailable: remote repo has no persisted clone")
	}

	info, err := os.Stat(repo.Path)
	if err != nil {
		return "", fmt.Errorf("repository path not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path is not a directory: %s", repo.Path)
	}
	return repo.Path, nil
}

// safeJoinPath safely joins a repository root with a repository-relative file
// path, rejecting path traversal attempts.
func safeJoinPath(repoRoot, relPath string) (string, error) {
	// Normalize: strip leading ./ and reject absolute paths
	relPath = strings.TrimPrefix(relPath, "./")
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path not allowed: %s", relPath)
	}

	joined := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repo root: %w", err)
	}
	// Ensure the resolved path is within the repo root
	if !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) && absJoined != absRoot {
		return "", fmt.Errorf("path traversal rejected: %s", relPath)
	}
	return absJoined, nil
}

// readSourceFile reads a file from disk within a repository.
func readSourceFile(repoRoot, relPath string) (string, error) {
	absPath, err := safeJoinPath(repoRoot, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return string(data), nil
}

func isBinarySource(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	return !utf8.Valid(sample)
}

func readSourceFileLimited(repoRoot, relPath string, maxBytes int64) (string, error) {
	absPath, err := safeJoinPath(repoRoot, relPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("reading file info: %w", err)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("file exceeds max size limit")
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	var reader io.Reader = f
	if maxBytes > 0 {
		reader = io.LimitReader(f, maxBytes+1)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return "", fmt.Errorf("file exceeds max size limit")
	}
	if isBinarySource(data) {
		return "", fmt.Errorf("binary file not supported")
	}
	return string(data), nil
}

// extractSymbolContext extracts the source lines for a symbol from file content.
func extractSymbolContext(content string, startLine, endLine int) string {
	lines := strings.Split(content, "\n")
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return ""
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}

// languageToProto converts a GraphQL Language enum value to the proto Language enum.
var languageToProtoMap = map[string]commonv1.Language{
	"GO":         commonv1.Language_LANGUAGE_GO,
	"PYTHON":     commonv1.Language_LANGUAGE_PYTHON,
	"TYPESCRIPT": commonv1.Language_LANGUAGE_TYPESCRIPT,
	"JAVASCRIPT": commonv1.Language_LANGUAGE_JAVASCRIPT,
	"JAVA":       commonv1.Language_LANGUAGE_JAVA,
	"RUST":       commonv1.Language_LANGUAGE_RUST,
	"CSHARP":     commonv1.Language_LANGUAGE_CSHARP,
	"CPP":        commonv1.Language_LANGUAGE_CPP,
	"RUBY":       commonv1.Language_LANGUAGE_RUBY,
	"PHP":        commonv1.Language_LANGUAGE_PHP,
}

func languageToProto(lang string) commonv1.Language {
	if v, ok := languageToProtoMap[strings.ToUpper(lang)]; ok {
		return v
	}
	return commonv1.Language_LANGUAGE_UNSPECIFIED
}

// deriveLanguage guesses the language from a file extension.
func deriveLanguage(filePath string) commonv1.Language {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return commonv1.Language_LANGUAGE_GO
	case ".py":
		return commonv1.Language_LANGUAGE_PYTHON
	case ".ts", ".tsx":
		return commonv1.Language_LANGUAGE_TYPESCRIPT
	case ".js", ".jsx":
		return commonv1.Language_LANGUAGE_JAVASCRIPT
	case ".java":
		return commonv1.Language_LANGUAGE_JAVA
	case ".rs":
		return commonv1.Language_LANGUAGE_RUST
	case ".cs":
		return commonv1.Language_LANGUAGE_CSHARP
	case ".cpp", ".cc", ".cxx", ".h", ".hpp":
		return commonv1.Language_LANGUAGE_CPP
	case ".rb":
		return commonv1.Language_LANGUAGE_RUBY
	case ".php":
		return commonv1.Language_LANGUAGE_PHP
	default:
		return commonv1.Language_LANGUAGE_UNSPECIFIED
	}
}

func discussionContextFromArtifact(artifact *knowledgepkg.Artifact) string {
	if artifact == nil || len(artifact.Sections) == 0 {
		return ""
	}
	scopePath := "repository"
	if artifact.Scope != nil {
		scopePath = artifact.Scope.ScopePath
	}
	parts := []string{
		fmt.Sprintf("Indexed %s context for %s.", strings.ToLower(string(artifact.Type)), scopePath),
	}
	for idx, section := range artifact.Sections {
		if idx >= 6 {
			break
		}
		body := section.Summary
		if body == "" {
			body = section.Content
		}
		body = strings.TrimSpace(body)
		if len(body) > 500 {
			body = body[:500] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s:\n%s", section.Title, body))
	}
	return strings.Join(parts, "\n\n")
}

func discussionContextFromStoredSymbol(sym *graphstore.StoredSymbol) string {
	if sym == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("Indexed symbol: %s", sym.QualifiedName),
	}
	if sym.Signature != "" {
		parts = append(parts, sym.Signature)
	}
	if sym.DocComment != "" {
		parts = append(parts, sym.DocComment)
	}
	return strings.Join(parts, "\n")
}

func protoCodeSymbolFromStored(sym *graphstore.StoredSymbol) *commonv1.CodeSymbol {
	if sym == nil {
		return nil
	}
	return &commonv1.CodeSymbol{
		Id:            sym.ID,
		Name:          sym.Name,
		QualifiedName: sym.QualifiedName,
		Kind:          commonv1.SymbolKind(commonv1.SymbolKind_value["SYMBOL_KIND_"+strings.ToUpper(sym.Kind)]),
		Language:      languageToProto(sym.Language),
		Location: &commonv1.FileLocation{
			Path:      sym.FilePath,
			StartLine: int32(sym.StartLine),
			EndLine:   int32(sym.EndLine),
		},
		Signature:  sym.Signature,
		DocComment: sym.DocComment,
	}
}

func shouldRefreshScopedCliffNotes(artifact *knowledgepkg.Artifact) bool {
	if artifact == nil || artifact.Type != knowledgepkg.ArtifactCliffNotes || artifact.Scope == nil {
		return false
	}
	if artifact.Scope.ScopeType != knowledgepkg.ScopeSymbol {
		return false
	}
	for _, section := range artifact.Sections {
		if section.Title == "Impact Analysis" {
			return false
		}
	}
	return true
}

func mapRepository(gr *graphstore.Repository) *Repository {
	status := RepositoryStatus(gr.Status)
	lastIndexed := gr.LastIndexedAt
	created := gr.CreatedAt
	repo := &Repository{
		ID:            gr.ID,
		Name:          gr.Name,
		Path:          gr.Path,
		HasAuth:       gr.AuthToken != "",
		Status:        status,
		FileCount:     gr.FileCount,
		FunctionCount: gr.FunctionCount,
		ClassCount:    gr.ClassCount,
		LastIndexedAt: &lastIndexed,
		CreatedAt:     created,
		Modules:       []*Module{},
	}
	if gr.RemoteURL != "" {
		repo.RemoteURL = &gr.RemoteURL
	}
	if gr.CommitSHA != "" {
		repo.CommitSha = &gr.CommitSHA
	}
	if gr.Branch != "" {
		repo.Branch = &gr.Branch
	}
	return repo
}

func mapSymbol(s *graphstore.StoredSymbol) *CodeSymbol {
	kind := SymbolKind(s.Kind)
	lang := Language(s.Language)
	return &CodeSymbol{
		ID:            s.ID,
		Name:          s.Name,
		QualifiedName: s.QualifiedName,
		Kind:          kind,
		Language:      lang,
		FilePath:      s.FilePath,
		StartLine:     s.StartLine,
		EndLine:       s.EndLine,
		Signature:     &s.Signature,
		DocComment:    &s.DocComment,
		Callers:       []*CodeSymbol{},
		Callees:       []*CodeSymbol{},
		Requirements:  []*RequirementLink{},
	}
}

func mapRequirement(r *graphstore.StoredRequirement) *Requirement {
	priority := r.Priority
	var priorityPtr *string
	if priority != "" {
		priorityPtr = &priority
	}
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	updated := r.UpdatedAt
	return &Requirement{
		ID:          r.ID,
		ExternalID:  &r.ExternalID,
		Title:       r.Title,
		Description: r.Description,
		Source:      r.Source,
		Priority:    priorityPtr,
		Tags:        tags,
		Links:       []*RequirementLink{},
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   &updated,
	}
}

func mapLink(l *graphstore.StoredLink) *RequirementLink {
	conf := confidenceFromFloat(l.Confidence)
	return &RequirementLink{
		ID:            l.ID,
		RequirementID: l.RequirementID,
		SymbolID:      l.SymbolID,
		Confidence:    conf,
		Rationale:     &l.Rationale,
		Verified:      l.Verified,
		VerifiedBy:    &l.VerifiedBy,
		CreatedAt:     l.CreatedAt,
	}
}

// mapLinkWithRelations maps a StoredLink and populates both Symbol and Requirement.
func mapLinkWithRelations(l *graphstore.StoredLink, store graphstore.GraphStore) *RequirementLink {
	rl := mapLink(l)
	if store == nil {
		return rl
	}
	if req := store.GetRequirement(l.RequirementID); req != nil {
		rl.Requirement = mapRequirement(req)
	}
	if sym := store.GetSymbol(l.SymbolID); sym != nil {
		rl.Symbol = mapSymbol(sym)
	}
	return rl
}

// mapLinksWithRelationsBatch maps a slice of StoredLinks with batch-fetched symbols and requirements.
func mapLinksWithRelationsBatch(links []*graphstore.StoredLink, store graphstore.GraphStore) []*RequirementLink {
	if len(links) == 0 {
		return []*RequirementLink{}
	}

	// Collect unique IDs
	symIDs := make([]string, 0, len(links))
	reqIDs := make([]string, 0, len(links))
	seenSym := map[string]bool{}
	seenReq := map[string]bool{}
	for _, l := range links {
		if !seenSym[l.SymbolID] {
			symIDs = append(symIDs, l.SymbolID)
			seenSym[l.SymbolID] = true
		}
		if !seenReq[l.RequirementID] {
			reqIDs = append(reqIDs, l.RequirementID)
			seenReq[l.RequirementID] = true
		}
	}

	symMap := store.GetSymbolsByIDs(symIDs)
	reqMap := store.GetRequirementsByIDs(reqIDs)

	result := make([]*RequirementLink, 0, len(links))
	for _, l := range links {
		rl := mapLink(l)
		if sym := symMap[l.SymbolID]; sym != nil {
			rl.Symbol = mapSymbol(sym)
		}
		if req := reqMap[l.RequirementID]; req != nil {
			rl.Requirement = mapRequirement(req)
		}
		result = append(result, rl)
	}
	return result
}

// populateRepositoryDetails fills in the files and modules for a repository.
func populateRepositoryDetails(repo *Repository, store graphstore.GraphStore) {
	if store == nil {
		return
	}
	mods := store.GetModules(repo.ID)
	repo.Modules = make([]*Module, 0, len(mods))
	for _, m := range mods {
		repo.Modules = append(repo.Modules, &Module{
			ID:        m.ID,
			Name:      m.Name,
			Path:      m.Path,
			FileCount: m.FileCount,
		})
	}
}

func mapFile(f *graphstore.File) *File {
	lang := Language(strings.ToUpper(f.Language))
	signals := f.AISignals
	if signals == nil {
		signals = []string{}
	}
	return &File{
		ID:        f.ID,
		Path:      f.Path,
		Language:  lang,
		LineCount: f.LineCount,
		AiScore:   f.AIScore,
		AiSignals: signals,
		Symbols:   []*CodeSymbol{},
	}
}

// populateSymbolRelations fills in callers, callees, and requirements for a symbol.
func populateSymbolRelations(sym *CodeSymbol, store graphstore.GraphStore) {
	if store == nil {
		return
	}
	// Callers
	callerIDs := store.GetCallers(sym.ID)
	sym.Callers = make([]*CodeSymbol, 0, len(callerIDs))
	for _, cid := range callerIDs {
		caller := store.GetSymbol(cid)
		if caller != nil {
			sym.Callers = append(sym.Callers, mapSymbol(caller))
		}
	}
	// Callees
	calleeIDs := store.GetCallees(sym.ID)
	sym.Callees = make([]*CodeSymbol, 0, len(calleeIDs))
	for _, cid := range calleeIDs {
		callee := store.GetSymbol(cid)
		if callee != nil {
			sym.Callees = append(sym.Callees, mapSymbol(callee))
		}
	}
	// Requirement links
	links := store.GetLinksForSymbol(sym.ID, false)
	sym.Requirements = make([]*RequirementLink, 0, len(links))
	for _, l := range links {
		rl := mapLink(l)
		// Populate the requirement reference
		req := store.GetRequirement(l.RequirementID)
		if req != nil {
			mapped := mapRequirement(req)
			rl.Requirement = mapped
		}
		sym.Requirements = append(sym.Requirements, rl)
	}
}

// populateRequirementLinks fills in the links for a requirement.
// Links are already sorted by confidence DESC from the store; we cap at 50
// for the UI detail view to avoid slow responses with thousands of links.
func populateRequirementLinks(req *Requirement, store graphstore.GraphStore) {
	if store == nil {
		return
	}
	links := store.GetLinksForRequirement(req.ID, false)
	if len(links) > 50 {
		links = links[:50]
	}

	// Batch-fetch all symbols in one query instead of N individual lookups.
	symIDs := make([]string, 0, len(links))
	for _, l := range links {
		symIDs = append(symIDs, l.SymbolID)
	}
	symMap := store.GetSymbolsByIDs(symIDs)

	req.Links = make([]*RequirementLink, 0, len(links))
	for _, l := range links {
		rl := mapLink(l)
		if sym := symMap[l.SymbolID]; sym != nil {
			rl.Symbol = mapSymbol(sym)
		}
		req.Links = append(req.Links, rl)
	}
}

// protoConfidenceToFloat converts a proto Confidence enum to a 0-1 float.
func protoConfidenceToFloat(c commonv1.Confidence) float64 {
	switch c {
	case commonv1.Confidence_CONFIDENCE_VERIFIED:
		return 1.0
	case commonv1.Confidence_CONFIDENCE_HIGH:
		return 0.85
	case commonv1.Confidence_CONFIDENCE_MEDIUM:
		return 0.65
	case commonv1.Confidence_CONFIDENCE_LOW:
		return 0.35
	default:
		return 0.0
	}
}

func confidenceFromFloat(c float64) Confidence {
	if c >= 1.0 {
		return ConfidenceVerified
	}
	if c >= 0.8 {
		return ConfidenceHigh
	}
	if c >= 0.5 {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

// ---------------------------------------------------------------------------
// Knowledge artifact mapping helpers
// ---------------------------------------------------------------------------

func mapKnowledgeArtifact(a *knowledgepkg.Artifact) *KnowledgeArtifact {
	out := &KnowledgeArtifact{
		ID:                      a.ID,
		RepositoryID:            a.RepositoryID,
		Type:                    mapArtifactType(a.Type),
		Audience:                mapAudience(a.Audience),
		Depth:                   mapDepth(a.Depth),
		Scope:                   mapArtifactScope(a.Scope),
		Status:                  mapArtifactStatus(a.Status),
		Progress:                a.Progress,
		RefreshAvailable:        a.Stale,
		UnderstandingID:         ptrString(a.UnderstandingID),
		UnderstandingRevisionFp: ptrString(a.UnderstandingRevisionFP),
		SourceRevision: &SourceRevision{
			CommitSha:          ptrString(a.SourceRevision.CommitSHA),
			Branch:             ptrString(a.SourceRevision.Branch),
			ContentFingerprint: ptrString(a.SourceRevision.ContentFingerprint),
			DocsFingerprint:    ptrString(a.SourceRevision.DocsFingerprint),
		},
		Stale:     a.Stale,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
	if !a.GeneratedAt.IsZero() {
		out.GeneratedAt = &a.GeneratedAt
	}
	if a.ProgressPhase != "" {
		out.ProgressPhase = ptrString(a.ProgressPhase)
	}
	if a.ProgressMessage != "" {
		out.ProgressMessage = ptrString(a.ProgressMessage)
	}
	if a.ErrorCode != "" {
		out.ErrorCode = ptrString(a.ErrorCode)
	}
	if a.ErrorMessage != "" {
		out.ErrorMessage = ptrString(a.ErrorMessage)
	}
	for _, sec := range a.Sections {
		out.Sections = append(out.Sections, mapKnowledgeSection(&sec))
	}
	if out.Sections == nil {
		out.Sections = []*KnowledgeSection{}
	}
	return out
}

func mapKnowledgeArtifactWithStore(store knowledgepkg.KnowledgeStore, a *knowledgepkg.Artifact) *KnowledgeArtifact {
	out := mapKnowledgeArtifact(a)
	if store == nil || a == nil || out == nil {
		return out
	}
	scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	if a.Scope != nil {
		scope = a.Scope.Normalize()
	}
	if u := store.GetRepositoryUnderstanding(a.RepositoryID, understandingScopeForArtifact(scope)); u != nil {
		out.RefreshAvailable = a.Stale || knowledgepkg.ArtifactRefreshAvailable(a, u)
		if out.UnderstandingID == nil && u.ID != "" {
			out.UnderstandingID = ptrString(u.ID)
		}
		if out.UnderstandingRevisionFp == nil && u.RevisionFP != "" {
			out.UnderstandingRevisionFp = ptrString(u.RevisionFP)
		}
	}
	return out
}

func mapRepositoryUnderstanding(u *knowledgepkg.RepositoryUnderstanding) *RepositoryUnderstanding {
	if u == nil {
		return nil
	}
	return &RepositoryUnderstanding{
		ID:               u.ID,
		RepositoryID:     u.RepositoryID,
		Scope:            mapArtifactScope(u.Scope),
		CorpusID:         ptrString(u.CorpusID),
		RevisionFp:       u.RevisionFP,
		Strategy:         ptrString(u.Strategy),
		Stage:            mapRepositoryUnderstandingStage(u.Stage),
		TreeStatus:       mapRepositoryUnderstandingTreeStatus(u.TreeStatus),
		CachedNodes:      u.CachedNodes,
		TotalNodes:       u.TotalNodes,
		ModelUsed:        ptrString(u.ModelUsed),
		RefreshAvailable: u.Stage == knowledgepkg.UnderstandingNeedsRefresh,
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        u.UpdatedAt,
		ErrorCode:        ptrString(u.ErrorCode),
		ErrorMessage:     ptrString(u.ErrorMessage),
	}
}

func mapArtifactScope(scope *knowledgepkg.ArtifactScope) *KnowledgeScope {
	if scope == nil {
		scope = &knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
	}
	norm := scope.Normalize()
	out := &KnowledgeScope{
		ScopeType: mapScopeType(norm.ScopeType),
		ScopePath: norm.ScopePath,
	}
	if norm.ScopeType == knowledgepkg.ScopeRepository {
		out.ScopePath = ""
	}
	if norm.ModulePath != "" {
		out.ModulePath = ptrString(norm.ModulePath)
	}
	if norm.FilePath != "" {
		out.FilePath = ptrString(norm.FilePath)
	}
	if norm.SymbolName != "" {
		out.SymbolName = ptrString(norm.SymbolName)
	}
	return out
}

func mapKnowledgeSection(s *knowledgepkg.Section) *KnowledgeSection {
	out := &KnowledgeSection{
		ID:         s.ID,
		ArtifactID: s.ArtifactID,
		Title:      s.Title,
		Content:    s.Content,
		Summary:    ptrString(s.Summary),
		Confidence: mapKnowledgeConfidence(s.Confidence),
		Inferred:   s.Inferred,
		OrderIndex: s.OrderIndex,
	}
	for _, ev := range s.Evidence {
		out.Evidence = append(out.Evidence, mapKnowledgeEvidence(&ev))
	}
	if out.Evidence == nil {
		out.Evidence = []*KnowledgeEvidence{}
	}
	return out
}

func mapKnowledgeEvidence(e *knowledgepkg.Evidence) *KnowledgeEvidence {
	return &KnowledgeEvidence{
		ID:         e.ID,
		SectionID:  e.SectionID,
		SourceType: mapEvidenceSourceType(e.SourceType),
		SourceID:   e.SourceID,
		FilePath:   ptrString(e.FilePath),
		LineStart:  ptrInt(e.LineStart),
		LineEnd:    ptrInt(e.LineEnd),
		Rationale:  ptrString(e.Rationale),
	}
}

func mapArtifactType(t knowledgepkg.ArtifactType) KnowledgeArtifactType {
	switch t {
	case knowledgepkg.ArtifactCliffNotes:
		return KnowledgeArtifactTypeCliffNotes
	case knowledgepkg.ArtifactLearningPath:
		return KnowledgeArtifactTypeLearningPath
	case knowledgepkg.ArtifactCodeTour:
		return KnowledgeArtifactTypeCodeTour
	case knowledgepkg.ArtifactWorkflowStory:
		return KnowledgeArtifactTypeWorkflowStory
	default:
		return KnowledgeArtifactTypeCliffNotes
	}
}

func mapArtifactStatus(s knowledgepkg.ArtifactStatus) KnowledgeArtifactStatus {
	switch s {
	case knowledgepkg.StatusPending:
		return KnowledgeArtifactStatusPending
	case knowledgepkg.StatusGenerating:
		return KnowledgeArtifactStatusGenerating
	case knowledgepkg.StatusReady:
		return KnowledgeArtifactStatusReady
	case knowledgepkg.StatusFailed:
		return KnowledgeArtifactStatusFailed
	case knowledgepkg.StatusStale:
		return KnowledgeArtifactStatusStale
	default:
		return KnowledgeArtifactStatusPending
	}
}

func mapAudience(a knowledgepkg.Audience) KnowledgeAudience {
	switch a {
	case knowledgepkg.AudienceBeginner:
		return KnowledgeAudienceBeginner
	case knowledgepkg.AudienceDeveloper:
		return KnowledgeAudienceDeveloper
	default:
		return KnowledgeAudienceDeveloper
	}
}

func mapDepth(d knowledgepkg.Depth) KnowledgeDepth {
	switch d {
	case knowledgepkg.DepthSummary:
		return KnowledgeDepthSummary
	case knowledgepkg.DepthMedium:
		return KnowledgeDepthMedium
	case knowledgepkg.DepthDeep:
		return KnowledgeDepthDeep
	default:
		return KnowledgeDepthMedium
	}
}

func mapScopeType(scopeType knowledgepkg.ScopeType) KnowledgeScopeType {
	switch scopeType {
	case knowledgepkg.ScopeModule:
		return KnowledgeScopeTypeModule
	case knowledgepkg.ScopeFile:
		return KnowledgeScopeTypeFile
	case knowledgepkg.ScopeSymbol:
		return KnowledgeScopeTypeSymbol
	case knowledgepkg.ScopeRequirement:
		return KnowledgeScopeTypeRequirement
	default:
		return KnowledgeScopeTypeRepository
	}
}

func mapKnowledgeConfidence(c knowledgepkg.ConfidenceLevel) KnowledgeConfidence {
	switch c {
	case knowledgepkg.ConfidenceHigh:
		return KnowledgeConfidenceHigh
	case knowledgepkg.ConfidenceMedium:
		return KnowledgeConfidenceMedium
	case knowledgepkg.ConfidenceLow:
		return KnowledgeConfidenceLow
	default:
		return KnowledgeConfidenceMedium
	}
}

func mapRepositoryUnderstandingStage(stage knowledgepkg.RepositoryUnderstandingStage) RepositoryUnderstandingStage {
	switch stage {
	case knowledgepkg.UnderstandingBuildingTree:
		return RepositoryUnderstandingStageBuildingTree
	case knowledgepkg.UnderstandingFirstPassReady:
		return RepositoryUnderstandingStageFirstPassReady
	case knowledgepkg.UnderstandingNeedsRefresh:
		return RepositoryUnderstandingStageNeedsRefresh
	case knowledgepkg.UnderstandingDeepening:
		return RepositoryUnderstandingStageDeepening
	case knowledgepkg.UnderstandingReady:
		return RepositoryUnderstandingStageReady
	case knowledgepkg.UnderstandingFailed:
		return RepositoryUnderstandingStageFailed
	default:
		return RepositoryUnderstandingStageBuildingTree
	}
}

func mapRepositoryUnderstandingTreeStatus(status knowledgepkg.RepositoryUnderstandingTreeStatus) RepositoryUnderstandingTreeStatus {
	switch status {
	case knowledgepkg.UnderstandingTreeMissing:
		return RepositoryUnderstandingTreeStatusMissing
	case knowledgepkg.UnderstandingTreePartial:
		return RepositoryUnderstandingTreeStatusPartial
	case knowledgepkg.UnderstandingTreeComplete:
		return RepositoryUnderstandingTreeStatusComplete
	default:
		return RepositoryUnderstandingTreeStatusMissing
	}
}

func mapEvidenceSourceType(t knowledgepkg.EvidenceSourceType) EvidenceSourceType {
	switch t {
	case knowledgepkg.EvidenceFile:
		return EvidenceSourceTypeFile
	case knowledgepkg.EvidenceSymbol:
		return EvidenceSourceTypeSymbol
	case knowledgepkg.EvidenceRequirement:
		return EvidenceSourceTypeRequirement
	case knowledgepkg.EvidenceDocumentation:
		return EvidenceSourceTypeDocumentation
	default:
		return EvidenceSourceTypeFile
	}
}

func mapProtoConfidence(c string) knowledgepkg.ConfidenceLevel {
	switch strings.ToLower(c) {
	case "high":
		return knowledgepkg.ConfidenceHigh
	case "medium":
		return knowledgepkg.ConfidenceMedium
	case "low":
		return knowledgepkg.ConfidenceLow
	default:
		return knowledgepkg.ConfidenceMedium
	}
}

func knowledgeAudiencePtr(a knowledgepkg.Audience) *KnowledgeAudience {
	v := mapAudience(a)
	return &v
}

func knowledgeDepthPtr(d knowledgepkg.Depth) *KnowledgeDepth {
	v := mapDepth(d)
	return &v
}

func ptrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptrInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func mapUnderstandingScore(s *graphstore.UnderstandingScore) *UnderstandingScore {
	return &UnderstandingScore{
		Overall:               s.Overall,
		TraceabilityCoverage:  s.TraceabilityCoverage,
		DocumentationCoverage: s.DocumentationCoverage,
		ReviewCoverage:        s.ReviewCoverage,
		TestCoverage:          s.TestCoverage,
		KnowledgeFreshness:    s.KnowledgeFreshness,
		AiCodeRatio:           s.AICodeRatio,
		ComputedAt:            s.ComputedAt,
	}
}

// ---------------------------------------------------------------------------
// Impact report mapping helpers
// ---------------------------------------------------------------------------

func mapImpactReport(r *graphstore.ImpactReport) *ImpactReport {
	out := &ImpactReport{
		ID:           r.ID,
		RepositoryID: r.RepositoryID,
		ComputedAt:   r.ComputedAt,
	}
	if r.OldCommitSHA != "" {
		out.OldCommitSha = &r.OldCommitSHA
	}
	if r.NewCommitSHA != "" {
		out.NewCommitSha = &r.NewCommitSHA
	}
	out.FilesChanged = make([]*FileDiff, 0, len(r.FilesChanged))
	for _, fd := range r.FilesChanged {
		out.FilesChanged = append(out.FilesChanged, mapFileDiff(&fd))
	}
	out.SymbolsAdded = mapSymbolChanges(r.SymbolsAdded)
	out.SymbolsModified = mapSymbolChanges(r.SymbolsModified)
	out.SymbolsRemoved = mapSymbolChanges(r.SymbolsRemoved)
	out.AffectedLinks = make([]*ImpactAffectedLink, 0, len(r.AffectedLinks))
	for _, al := range r.AffectedLinks {
		out.AffectedLinks = append(out.AffectedLinks, &ImpactAffectedLink{
			LinkID:        al.LinkID,
			RequirementID: al.RequirementID,
			SymbolID:      al.SymbolID,
			Impact:        al.Impact,
			Confidence:    al.Confidence,
		})
	}
	out.AffectedRequirements = make([]*ImpactAffectedRequirement, 0, len(r.AffectedRequirements))
	for _, ar := range r.AffectedRequirements {
		out.AffectedRequirements = append(out.AffectedRequirements, &ImpactAffectedRequirement{
			RequirementID: ar.RequirementID,
			ExternalID:    ar.ExternalID,
			Title:         ar.Title,
			AffectedLinks: ar.AffectedLinks,
			TotalLinks:    ar.TotalLinks,
		})
	}
	out.StaleArtifacts = r.StaleArtifacts
	if out.StaleArtifacts == nil {
		out.StaleArtifacts = []string{}
	}
	return out
}

func mapFileDiff(fd *graphstore.ImpactFileDiff) *FileDiff {
	out := &FileDiff{
		Path:      fd.Path,
		Status:    mapDiffStatus(fd.Status),
		Additions: fd.Additions,
		Deletions: fd.Deletions,
	}
	if fd.OldPath != "" {
		out.OldPath = &fd.OldPath
	}
	return out
}

func mapDiffStatus(s string) DiffStatus {
	switch strings.ToLower(s) {
	case "added":
		return DiffStatusAdded
	case "modified":
		return DiffStatusModified
	case "deleted":
		return DiffStatusDeleted
	case "renamed":
		return DiffStatusRenamed
	default:
		return DiffStatusModified
	}
}

func mapSymbolChanges(changes []graphstore.ImpactSymbolChange) []*SymbolChange {
	out := make([]*SymbolChange, 0, len(changes))
	for _, sc := range changes {
		mapped := &SymbolChange{
			Name:     sc.Name,
			FilePath: sc.FilePath,
		}
		if sc.SymbolID != "" {
			mapped.SymbolID = &sc.SymbolID
		}
		switch strings.ToLower(sc.ChangeType) {
		case "added":
			mapped.ChangeType = SymbolChangeTypeAdded
		case "modified":
			mapped.ChangeType = SymbolChangeTypeModified
		case "removed":
			mapped.ChangeType = SymbolChangeTypeRemoved
		default:
			mapped.ChangeType = SymbolChangeTypeModified
		}
		if sc.OldSignature != "" {
			mapped.OldSignature = &sc.OldSignature
		}
		if sc.NewSignature != "" {
			mapped.NewSignature = &sc.NewSignature
		}
		out = append(out, mapped)
	}
	return out
}

// knowledgeFreshnessAdapter bridges KnowledgeStore to KnowledgeFreshnessProvider.
type knowledgeFreshnessAdapter struct {
	store knowledgepkg.KnowledgeStore
}

func (a *knowledgeFreshnessAdapter) GetFreshnessRatio(repoID string) (fresh int, total int) {
	artifacts := a.store.GetKnowledgeArtifacts(repoID)
	total = len(artifacts)
	for _, art := range artifacts {
		if !art.Stale && art.Status == knowledgepkg.StatusReady {
			fresh++
		}
	}
	return
}

// newKnowledgeFreshnessProvider wraps a KnowledgeStore as a KnowledgeFreshnessProvider.
// Returns nil if the store is nil.
func newKnowledgeFreshnessProvider(store knowledgepkg.KnowledgeStore) graphstore.KnowledgeFreshnessProvider {
	if store == nil {
		return nil
	}
	return &knowledgeFreshnessAdapter{store: store}
}

func mapDiscoveredRequirement(d *graphstore.DiscoveredRequirement) *DiscoveredRequirement {
	if d == nil {
		return nil
	}
	sourceFiles := d.SourceFiles
	if sourceFiles == nil {
		sourceFiles = []string{}
	}
	keywords := d.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	var promotedTo, dismissedBy, dismissedReason *string
	if d.PromotedTo != "" {
		promotedTo = &d.PromotedTo
	}
	if d.DismissedBy != "" {
		dismissedBy = &d.DismissedBy
	}
	if d.DismissedReason != "" {
		dismissedReason = &d.DismissedReason
	}
	return &DiscoveredRequirement{
		ID:              d.ID,
		RepoID:          d.RepoID,
		Source:          d.Source,
		SourceFile:      d.SourceFile,
		SourceLine:      d.SourceLine,
		SourceFiles:     sourceFiles,
		Text:            d.Text,
		RawText:         d.RawText,
		GroupKey:        d.GroupKey,
		Language:        d.Language,
		Keywords:        keywords,
		Confidence:      d.Confidence,
		Status:          d.Status,
		LlmRefined:      d.LLMRefined,
		PromotedTo:      promotedTo,
		DismissedBy:     dismissedBy,
		DismissedReason: dismissedReason,
		CreatedAt:       d.CreatedAt,
	}
}

func mapCrossRepoRefs(refs []*graphstore.CrossRepoRef) []*CrossRepoRef {
	result := make([]*CrossRepoRef, 0, len(refs))
	for _, r := range refs {
		ref := &CrossRepoRef{
			ID:             r.ID,
			SourceSymbolID: r.SourceSymbolID,
			TargetSymbolID: r.TargetSymbolID,
			SourceRepoID:   r.SourceRepoID,
			TargetRepoID:   r.TargetRepoID,
			RefType:        CrossRepoRefType(r.RefType),
			Confidence:     r.Confidence,
			CreatedAt:      r.CreatedAt,
		}
		if r.ContractFile != "" {
			ref.ContractFile = &r.ContractFile
		}
		if r.ConsumerFile != "" {
			ref.ConsumerFile = &r.ConsumerFile
		}
		if r.Evidence != "" {
			ref.Evidence = &r.Evidence
		}
		result = append(result, ref)
	}
	return result
}

func mapDiagramOutput(r *architecture.DiagramResult) *DiagramOutput {
	level := DiagramLevel(r.Level)
	modules := make([]*DiagramModule, 0, len(r.Modules))
	for _, m := range r.Modules {
		edges := make([]*DiagramEdge, 0, len(m.OutboundEdges))
		for _, e := range m.OutboundEdges {
			edges = append(edges, &DiagramEdge{
				TargetPath: e.TargetPath,
				CallCount:  e.CallCount,
			})
		}
		modules = append(modules, &DiagramModule{
			Path:                 m.Path,
			SymbolCount:          m.SymbolCount,
			FileCount:            m.FileCount,
			RequirementLinkCount: m.RequirementLinkCount,
			InboundEdgeCount:     m.InboundEdgeCount,
			OutboundEdges:        edges,
		})
	}
	return &DiagramOutput{
		MermaidSource: r.MermaidSource,
		Modules:       modules,
		Level:         level,
		TotalModules:  r.TotalModules,
		ShownModules:  r.ShownModules,
		Truncated:     r.Truncated,
	}
}
