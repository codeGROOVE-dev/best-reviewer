# Reviewer Package

This package provides intelligent reviewer selection for GitHub pull requests.

## Architecture

The package is organized into focused modules:

- **finder.go**: Main `Finder` struct with the public `Find()` API
- **constants.go**: Configuration constants and selection method names
- **helpers.go**: Internal helper functions and types
- **finders.go**: Various finder methods (directory, project, assignee)
- **scoring.go**: Scoring algorithms for ranking candidates
- **overlap.go**: Line overlap detection for finding related code authors
- **progressive.go**: Progressive loading strategy to minimize API calls
- **optimized.go**: Optimized finding strategies with recency bias
- **graphql.go**: GraphQL queries for PR history and activity
- **output.go**: Output formatting utilities

## Usage

```go
import (
    "context"
    "github.com/codeGROOVE-dev/best-reviewer/pkg/reviewer"
    "github.com/codeGROOVE-dev/best-reviewer/pkg/github"
    "github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// Create GitHub client
githubClient, err := github.New(ctx, github.Config{
    UseAppAuth: true,
    AppID: "12345",
    AppKeyPath: "/path/to/key.pem",
})

// Create reviewer finder
finder := reviewer.New(githubClient, reviewer.Config{
    MaxPRs: 5,
    PRCountCache: 6 * time.Hour,
})

// Find reviewers for a PR
candidates, err := finder.Find(ctx, pr)
if err != nil {
    log.Fatal(err)
}

for _, candidate := range candidates {
    fmt.Printf("Reviewer: %s (method: %s, score: %d)\n", 
        candidate.Username, 
        candidate.SelectionMethod, 
        candidate.ContextScore)
}
```

## Selection Methods

The package uses multiple strategies to find the best reviewers, in order of priority:

1. **Assignees** (`assignee-expert`): PR assignees who have write access
2. **Code Owners** (`codeowner`): Users defined in CODEOWNERS file
3. **Line Overlap** (`author-overlap`, `reviewer-overlap`): Users who modified the same lines
4. **File History** (`file-author`, `file-reviewer`): Recent contributors to the same files
5. **Directory Activity** (`author-directory`, `reviewer-directory`): Active in same directories
6. **Project Activity** (`author-project`, `reviewer-project`): Active in the repository
7. **Top Contributors** (`top-contributor`): Repository's most active contributors

## Progressive Loading

The finder uses a progressive strategy to minimize API calls:

1. Check assignees (no API call)
2. Check CODEOWNERS (cached)
3. Check file history with line overlap (targeted queries)
4. Check directory reviewers (cached)
5. Check top contributors (cached)
6. Full analysis as last resort

## Scoring Algorithm

Candidates are scored based on three factors:

- **File Overlap** (40%): How much they've worked on the changed files
- **Recency** (35%): When they last contributed (decay over time)
- **Domain Expertise** (25%): Previous work in the same area

## Dependencies

This package depends on:

- `github.com/codeGROOVE-dev/best-reviewer/pkg/types`: Shared data structures
- `github.com/codeGROOVE-dev/best-reviewer/pkg/cache`: Caching layer
- `github.com/codeGROOVE-dev/best-reviewer/pkg/github`: GitHub API client

## Implementation Status

**Completed:**
- ✅ Constants and configuration
- ✅ Helper functions
- ✅ Main Finder struct and Find() method
- ✅ Basic finder methods (assignee, directory, project)
- ✅ Scoring framework

**Remaining Work:**
- ⏳ overlap.go - Line overlap detection (needs porting)
- ⏳ progressive.go - Progressive loading (needs porting)
- ⏳ optimized.go - Optimized strategies (needs porting)
- ⏳ graphql.go - GraphQL queries (needs github.Client enhancement)
- ⏳ output.go - Output formatting (needs porting)
- ⏳ user_activity.go - User activity tracking (needs graphql support)

## Next Steps

To complete the package extraction:

1. **Add GraphQL support to pkg/github/client.go**:
   - Add `MakeGraphQLRequest(ctx, query, variables)` method
   - This is needed for efficient PR history queries

2. **Port remaining files**:
   - overlap.go: Line overlap detection logic
   - progressive.go: Progressive loading with CODEOWNERS support
   - optimized.go: Weighted selection and recency-based filtering
   - graphql.go: PR history queries using GraphQL
   - output.go: Formatting utilities

3. **Add missing GitHub client methods**:
   - Some methods like `fetchRepoUserActivity` need to be added to support scoring

4. **Update main code**:
   - Replace direct ReviewerFinder usage with pkg/reviewer.Finder
   - Update imports across the codebase

## Notes

- The package follows Go best practices with single responsibility per file
- All exported types and functions are documented
- Internal helpers are unexported
- Configuration is centralized in constants.go
- The API is designed to be simple: `finder.Find(ctx, pr)` returns candidates
