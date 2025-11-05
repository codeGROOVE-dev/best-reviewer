package reviewer

import (
	"context"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/internal/testutil"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

func TestFinder_findReviewersOptimized_NoFiles(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:        "test-owner",
		Repository:   "test-repo",
		Number:       1,
		Author:       "alice",
		Assignees:    []string{},
		ChangedFiles: []types.ChangedFile{}, // No files changed
	}

	reviewers := finder.findReviewersOptimized(ctx, pr)

	// Should return empty list if no files and no assignees
	if len(reviewers) != 0 {
		t.Errorf("expected 0 reviewers with no files, got %d", len(reviewers))
	}
}

// Note: collectWeightedCandidates is tested indirectly through integration tests
// as it requires complex GraphQL blame API mocking

// Note: topChangedFilesFiltered only filters lock files, not vendor/node_modules directories.
// That filtering is already tested in optimized_test.go

func TestFinder_getChangedLines_MultipleSections(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		ChangedFiles: []types.ChangedFile{
			{
				Filename: "main.go",
				Patch: `@@ -10,5 +10,7 @@ func main() {
 unchanged
+added1
 unchanged
@@ -50,3 +52,5 @@ func other() {
 unchanged
+added2
+added3
`,
			},
		},
	}

	lines, err := finder.getChangedLines(pr, "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should extract multiple line ranges
	if len(lines) < 2 {
		t.Errorf("expected at least 2 line ranges, got %d", len(lines))
	}

	// First range should start around line 10
	if len(lines) > 0 && lines[0][0] < 5 {
		t.Errorf("first range should start around line 10, got %d", lines[0][0])
	}
}

func TestFinder_getChangedLines_NoPatch(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		ChangedFiles: []types.ChangedFile{
			{
				Filename: "binary.png",
				Patch:    "", // No patch for binary files
			},
		},
	}

	lines, err := finder.getChangedLines(pr, "binary.png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != 0 {
		t.Errorf("expected 0 line ranges for file without patch, got %d", len(lines))
	}
}

// Note: recentCommitsInDirectory and recentPRsInProject require full GraphQL query matching
// which is impractical to mock. These are tested via integration tests.

func TestFinder_parseDirectoryCommitsFromGraphQL_HappyPath(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"history": map[string]any{
							"nodes": []any{
								map[string]any{
									"oid":             "abc123",
									"messageHeadline": "Fix bug",
									"author": map[string]any{
										"user": map[string]any{
											"login": "alice",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{
											map[string]any{
												"number":   float64(42),
												"merged":   true,
												"mergedAt": time.Now().Format(time.RFC3339),
												"author": map[string]any{
													"login": "alice",
												},
												"mergedBy": map[string]any{
													"login": "bob",
												},
												"reviews": map[string]any{
													"nodes": []any{
														map[string]any{
															"author": map[string]any{
																"login": "charlie",
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	prs := finder.parseDirectoryCommitsFromGraphQL(result)

	if len(prs) != 1 {
		t.Errorf("expected 1 PR, got %d", len(prs))
		return
	}

	if prs[0].Number != 42 {
		t.Errorf("expected PR 42, got %d", prs[0].Number)
	}

	if prs[0].Author != "alice" {
		t.Errorf("expected author alice, got %s", prs[0].Author)
	}

	if prs[0].MergedBy != "bob" {
		t.Errorf("expected mergedBy bob, got %s", prs[0].MergedBy)
	}
}

func TestFinder_parseProjectPRsFromGraphQL_HappyPath(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"nodes": []any{
						map[string]any{
							"number":   float64(200),
							"merged":   true,
							"mergedAt": time.Now().Format(time.RFC3339),
							"author": map[string]any{
								"login": "dave",
							},
							"mergedBy": map[string]any{
								"login": "eve",
							},
							"reviews": map[string]any{
								"nodes": []any{
									map[string]any{
										"author": map[string]any{
											"login": "frank",
										},
									},
									map[string]any{
										"author": map[string]any{
											"login": "grace",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	prs, err := finder.parseProjectPRsFromGraphQL(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 1 {
		t.Errorf("expected 1 PR, got %d", len(prs))
	}

	if prs[0].Number != 200 {
		t.Errorf("expected PR 200, got %d", prs[0].Number)
	}

	if prs[0].Author != "dave" {
		t.Errorf("expected author dave, got %s", prs[0].Author)
	}

	if len(prs[0].Reviewers) != 2 {
		t.Errorf("expected 2 reviewers, got %d", len(prs[0].Reviewers))
	}
}

func TestFinder_parseProjectPRsFromGraphQL_EmptyData(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{}

	_, err := finder.parseProjectPRsFromGraphQL(result)
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestFinder_parseProjectPRsFromGraphQL_NoNodes(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"nodes": []any{},
				},
			},
		},
	}

	prs, err := finder.parseProjectPRsFromGraphQL(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 0 {
		t.Errorf("expected 0 PRs, got %d", len(prs))
	}
}

func TestFinder_parseDirectoryCommitsFromGraphQL_NoData(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{}

	prs := finder.parseDirectoryCommitsFromGraphQL(result)
	if len(prs) != 0 {
		t.Errorf("expected 0 PRs for empty result, got %d", len(prs))
	}
}

func TestFinder_parseDirectoryCommitsFromGraphQL_NoDefaultBranchRef(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{},
		},
	}

	prs := finder.parseDirectoryCommitsFromGraphQL(result)
	if len(prs) != 0 {
		t.Errorf("expected 0 PRs when no defaultBranchRef, got %d", len(prs))
	}
}

func TestFinder_parseDirectoryCommitsFromGraphQL_DirectCommit(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Simulate a direct commit without a PR
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"history": map[string]any{
							"nodes": []any{
								map[string]any{
									"oid":             "abc123",
									"messageHeadline": "Direct commit",
									"author": map[string]any{
										"user": map[string]any{
											"login": "bob",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{}, // No PR
									},
								},
							},
						},
					},
				},
			},
		},
	}

	prs := finder.parseDirectoryCommitsFromGraphQL(result)

	// Should create a PR entry for the commit author
	if len(prs) != 1 {
		t.Errorf("expected 1 PR entry for direct commit, got %d", len(prs))
		return
	}

	if prs[0].Number != 0 {
		t.Errorf("expected PR number 0 for direct commit, got %d", prs[0].Number)
	}

	if prs[0].Author != "bob" {
		t.Errorf("expected author bob, got %s", prs[0].Author)
	}
}
