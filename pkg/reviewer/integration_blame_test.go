package reviewer

import (
	"context"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/internal/testutil"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// TestIntegration_blameForLines tests the complete blame API flow
func TestIntegration_blameForLines(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup comprehensive GraphQL blame response
	blameResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								// Range that overlaps with changed lines (10-20)
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"oid": "abc123",
										"author": map[string]any{
											"user": map[string]any{
												"login": "alice",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(100),
													"merged":   true,
													"mergedAt": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
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
								// Range that doesn't overlap (50-60)
								map[string]any{
									"startingLine": float64(50),
									"endingLine":   float64(60),
									"commit": map[string]any{
										"oid": "def456",
										"author": map[string]any{
											"user": map[string]any{
												"login": "dave",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(200),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "dave",
													},
													"mergedBy": map[string]any{
														"login": "eve",
													},
													"reviews": map[string]any{
														"nodes": []any{},
													},
												},
											},
										},
									},
								},
								// Range that partially overlaps (18-25)
								map[string]any{
									"startingLine": float64(18),
									"endingLine":   float64(25),
									"commit": map[string]any{
										"oid": "ghi789",
										"author": map[string]any{
											"user": map[string]any{
												"login": "frank",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(300),
													"merged":   true,
													"mergedAt": time.Now().Add(-72 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "frank",
													},
													"mergedBy": map[string]any{
														"login": "bob",
													},
													"reviews": map[string]any{
														"nodes": []any{
															map[string]any{
																"author": map[string]any{
																	"login": "alice",
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
		},
	}

	// Set the GraphQL response - using the exact query structure
	client.SetGraphQLResponse(`
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						blame(path: $path) {
							ranges {
								startingLine
								endingLine
								commit {
									oid
									author {
										user {
											login
										}
									}
									associatedPullRequests(first: 1) {
										nodes {
											number
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`, blameResponse)

	// Test with line ranges that should match
	lineRanges := [][2]int{{10, 20}, {15, 22}}

	overlappingPRs, allPRs, err := finder.blameForLines(ctx, "owner", "repo", "main.go", lineRanges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find 2 overlapping PRs (PR 100 and PR 300)
	if len(overlappingPRs) != 2 {
		t.Errorf("expected 2 overlapping PRs, got %d", len(overlappingPRs))
	}

	// Should find 1 non-overlapping PR (PR 200)
	if len(allPRs) != 1 {
		t.Errorf("expected 1 non-overlapping PR, got %d", len(allPRs))
	}

	// Verify PR details
	if len(overlappingPRs) > 0 {
		if overlappingPRs[0].Number != 100 && overlappingPRs[0].Number != 300 {
			t.Errorf("expected PR 100 or 300 in overlapping, got %d", overlappingPRs[0].Number)
		}
	}
}

// TestIntegration_collectWeightedCandidates tests the full candidate collection with blame
func TestIntegration_collectWeightedCandidates(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup blame response for file analysis
	blameResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(20),
									"commit": map[string]any{
										"oid": "abc123",
										"author": map[string]any{
											"user": map[string]any{
												"login": "expert-dev",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(50),
													"merged":   true,
													"mergedAt": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "expert-dev",
													},
													"mergedBy": map[string]any{
														"login": "maintainer",
													},
													"reviews": map[string]any{
														"nodes": []any{
															map[string]any{
																"author": map[string]any{
																	"login": "senior-dev",
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
		},
	}

	// Set the exact query from graphql.go (line 24)
	blameQuery := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						blame(path: $path) {
							ranges {
								startingLine
								endingLine
								commit {
									oid
									author {
										user {
											login
										}
									}
									associatedPullRequests(first: 1) {
										nodes {
											number
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(blameQuery, blameResponse)

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
		ChangedFiles: []types.ChangedFile{
			{
				Filename:  "main.go",
				Additions: 10,
				Deletions: 5,
				Patch:     "@@ -10,5 +10,7 @@ func main() {\n unchanged\n+added\n unchanged",
			},
		},
	}

	files := []string{"main.go"}

	candidates := finder.collectWeightedCandidates(ctx, pr, files)

	// Should have candidates from blame analysis
	if len(candidates) == 0 {
		t.Error("expected candidates from blame analysis, got none")
	}

	// Check that we found reviewers from the PR
	foundExpert := false
	foundSenior := false
	for _, c := range candidates {
		if c.username == "expert-dev" || c.username == "senior-dev" {
			if c.username == "expert-dev" {
				foundExpert = true
			}
			if c.username == "senior-dev" {
				foundSenior = true
			}
			if c.weight <= 0 {
				t.Errorf("expected positive weight for %s, got %d", c.username, c.weight)
			}
		}
	}

	if !foundExpert && !foundSenior {
		t.Error("expected to find expert-dev or senior-dev in candidates")
	}
}

// TestIntegration_collectWeightedCandidates_FileLevelContributions tests file-level scoring
func TestIntegration_collectWeightedCandidates_FileLevelContributions(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup blame response with both overlapping and non-overlapping PR contributions
	blameResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								// Overlapping range (lines 10-15, changed lines are 10-12)
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"oid": "overlap123",
										"author": map[string]any{
											"user": map[string]any{
												"login": "overlapping-dev",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(100),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "overlapping-dev",
													},
													"mergedBy": map[string]any{
														"login": "overlap-merger",
													},
													"reviews": map[string]any{
														"nodes": []any{},
													},
												},
											},
										},
									},
								},
								// Non-overlapping range (lines 50-60, changed lines are 10-12)
								map[string]any{
									"startingLine": float64(50),
									"endingLine":   float64(60),
									"commit": map[string]any{
										"oid": "file123",
										"author": map[string]any{
											"user": map[string]any{
												"login": "file-author",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(200),
													"merged":   true,
													"mergedAt": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "file-author",
													},
													"mergedBy": map[string]any{
														"login": "file-merger",
													},
													"reviews": map[string]any{
														"nodes": []any{
															map[string]any{
																"author": map[string]any{
																	"login": "file-reviewer",
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
		},
	}

	blameQuery := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						blame(path: $path) {
							ranges {
								startingLine
								endingLine
								commit {
									oid
									author {
										user {
											login
										}
									}
									associatedPullRequests(first: 1) {
										nodes {
											number
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(blameQuery, blameResponse)

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
		ChangedFiles: []types.ChangedFile{
			{
				Filename:  "main.go",
				Additions: 3,
				Deletions: 0,
				Patch:     "@@ -10,0 +10,3 @@ func main() {\n+line1\n+line2\n+line3",
			},
		},
	}

	files := []string{"main.go"}

	candidates := finder.collectWeightedCandidates(ctx, pr, files)

	if len(candidates) == 0 {
		t.Fatal("expected candidates from blame analysis, got none")
	}

	// Check overlapping candidates (should have higher weight)
	foundOverlappingDev := false
	foundOverlapMerger := false
	// Check file-level candidates (should have lower weight)
	foundFileAuthor := false
	foundFileMerger := false
	foundFileReviewer := false

	for _, c := range candidates {
		switch c.username {
		case "overlapping-dev":
			foundOverlappingDev = true
			// Should have line count weight (3 lines)
			if c.weight < 3 {
				t.Errorf("expected overlapping-dev weight >= 3, got %d", c.weight)
			}
		case "overlap-merger":
			foundOverlapMerger = true
			// Should have 2x line count weight
			if c.weight < 6 {
				t.Errorf("expected overlap-merger weight >= 6, got %d", c.weight)
			}
		case "file-author":
			foundFileAuthor = true
			// Should have file weight (5)
			if c.weight != 5 {
				t.Errorf("expected file-author weight = 5, got %d", c.weight)
			}
		case "file-merger":
			foundFileMerger = true
			// Should have 2x file weight (10)
			if c.weight != 10 {
				t.Errorf("expected file-merger weight = 10, got %d", c.weight)
			}
		case "file-reviewer":
			foundFileReviewer = true
			// Should have file weight (5)
			if c.weight != 5 {
				t.Errorf("expected file-reviewer weight = 5, got %d", c.weight)
			}
		}
	}

	if !foundOverlappingDev {
		t.Error("expected to find overlapping-dev in candidates")
	}
	if !foundOverlapMerger {
		t.Error("expected to find overlap-merger in candidates")
	}
	if !foundFileAuthor {
		t.Error("expected to find file-author in candidates")
	}
	if !foundFileMerger {
		t.Error("expected to find file-merger in candidates")
	}
	if !foundFileReviewer {
		t.Error("expected to find file-reviewer in candidates")
	}
}

// TestIntegration_findReviewersOptimized_WithBlame tests the full optimized flow
func TestIntegration_findReviewersOptimized_WithBlame(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup comprehensive responses for all API calls

	// 1. Blame API response
	blameResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(20),
									"commit": map[string]any{
										"oid": "commit1",
										"author": map[string]any{
											"user": map[string]any{
												"login": "bob",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(10),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "bob",
													},
													"mergedBy": map[string]any{
														"login": "charlie",
													},
													"reviews": map[string]any{
														"nodes": []any{
															map[string]any{
																"author": map[string]any{
																	"login": "dave",
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
		},
	}

	// Set the exact query from graphql.go (line 24)
	blameQuery := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						blame(path: $path) {
							ranges {
								startingLine
								endingLine
								commit {
									oid
									author {
										user {
											login
										}
									}
									associatedPullRequests(first: 1) {
										nodes {
											number
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(blameQuery, blameResponse)

	// Setup PR with changed files
	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
		Assignees:  []string{"bob"},
		ChangedFiles: []types.ChangedFile{
			{
				Filename:  "main.go",
				Additions: 10,
				Deletions: 5,
				Patch:     "@@ -10,5 +10,7 @@ func main() {\n line1\n+line2\n line3",
			},
		},
	}

	// Configure collaborators
	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob", "charlie", "dave", "eve"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)
	client.SetBotUser("charlie", false)
	client.SetBotUser("dave", false)
	client.SetBotUser("eve", false)

	// Set write access for all collaborators
	client.SetWriteAccess("test-owner", "test-repo", "bob", true)
	client.SetWriteAccess("test-owner", "test-repo", "charlie", true)
	client.SetWriteAccess("test-owner", "test-repo", "dave", true)

	// Set open PR counts for workload balancing
	client.SetOpenPRCount("test-owner", "bob", 1)
	client.SetOpenPRCount("test-owner", "charlie", 2)
	client.SetOpenPRCount("test-owner", "dave", 0)

	reviewers := finder.findReviewersOptimized(ctx, pr)

	if len(reviewers) == 0 {
		t.Error("expected reviewers from optimized search, got none")
	}

	// Should include assignee (bob)
	foundBob := false
	for _, r := range reviewers {
		if r.Username == "bob" {
			foundBob = true
			if r.ContextScore < 100 {
				t.Errorf("expected high context score for assignee, got %d", r.ContextScore)
			}
		}
	}

	if !foundBob {
		t.Error("expected assignee 'bob' in reviewers")
	}
}

// TestIntegration_blameForLines_EmptyRanges tests edge case
func TestIntegration_blameForLines_EmptyRanges(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	overlapping, all, err := finder.blameForLines(ctx, "owner", "repo", "file.go", [][2]int{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for empty line ranges")
	}
}

// TestIntegration_blameForLines_GraphQLError tests error handling
func TestIntegration_blameForLines_GraphQLError(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup error response
	errorResponse := map[string]any{
		"errors": []any{
			map[string]any{
				"message": "Resource not accessible by integration",
			},
		},
	}

	client.SetGraphQLResponse("query", errorResponse)

	// Should handle GraphQL errors gracefully
	overlapping, all, err := finder.blameForLines(ctx, "owner", "repo", "file.go", [][2]int{{1, 10}})
	if err != nil {
		t.Fatalf("unexpected error (should handle GraphQL errors gracefully): %v", err)
	}

	// May return empty results due to GraphQL error
	_ = overlapping
	_ = all
}
