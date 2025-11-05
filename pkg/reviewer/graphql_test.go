package reviewer

import (
	"context"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/internal/testutil"
)

func TestFinder_parseBlameResults_HappyPath(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Simulate a GraphQL blame response (matches actual structure from graphql.go)
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"oid": "abc123",
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(42),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
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
		},
	}

	lineRanges := [][2]int{{10, 20}}
	overlappingPRs, allPRs := finder.parseBlameResults(result, lineRanges)

	// Should find PRs that overlap with the line range
	if len(overlappingPRs) != 1 {
		t.Fatalf("expected 1 overlapping PR, got %d", len(overlappingPRs))
	}

	// allPRs contains only non-overlapping PRs, so should be empty
	if len(allPRs) != 0 {
		t.Errorf("expected 0 in allPRs (overlapping PRs don't go there), got %d", len(allPRs))
	}

	if overlappingPRs[0].Number != 42 {
		t.Errorf("expected PR number 42, got %d", overlappingPRs[0].Number)
	}

	if overlappingPRs[0].Author != "alice" {
		t.Errorf("expected author alice, got %s", overlappingPRs[0].Author)
	}
}

func TestFinder_parseBlameResults_NoOverlap(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(100),
									"endingLine":   float64(110),
									"commit": map[string]any{
										"oid": "abc123",
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(42),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
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
	}

	lineRanges := [][2]int{{10, 20}} // No overlap with 100-110
	overlappingPRs, allPRs := finder.parseBlameResults(result, lineRanges)

	if len(overlappingPRs) != 0 {
		t.Errorf("expected 0 overlapping PRs, got %d", len(overlappingPRs))
	}

	if len(allPRs) != 1 {
		t.Errorf("expected 1 total PR (even without overlap), got %d", len(allPRs))
	}
}

func TestFinder_parseBlameResults_NoDataField(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Empty result - no data field
	result := map[string]any{}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 {
		t.Errorf("expected 0 overlapping PRs, got %d", len(overlapping))
	}
	if len(all) != 0 {
		t.Errorf("expected 0 all PRs, got %d", len(all))
	}
}

func TestFinder_parseBlameResults_NoRepositoryField(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Missing repository field
	result := map[string]any{
		"data": map[string]any{},
	}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for missing repository field")
	}
}

func TestFinder_parseBlameResults_NoBlameField(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Missing blame field
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{},
				},
			},
		},
	}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for missing blame field")
	}
}

func TestExtractReviewers_NoReviews(t *testing.T) {
	pr := map[string]any{}
	reviewers := extractReviewers(pr)

	if len(reviewers) != 0 {
		t.Errorf("expected 0 reviewers, got %d", len(reviewers))
	}
}

func TestExtractReviewers_NoNodes(t *testing.T) {
	pr := map[string]any{
		"reviews": map[string]any{},
	}
	reviewers := extractReviewers(pr)

	if len(reviewers) != 0 {
		t.Errorf("expected 0 reviewers, got %d", len(reviewers))
	}
}

func TestExtractReviewers_InvalidNode(t *testing.T) {
	pr := map[string]any{
		"reviews": map[string]any{
			"nodes": []any{
				"not a map",
			},
		},
	}
	reviewers := extractReviewers(pr)

	if len(reviewers) != 0 {
		t.Errorf("expected 0 reviewers, got %d", len(reviewers))
	}
}

func TestExtractReviewers_NoAuthor(t *testing.T) {
	pr := map[string]any{
		"reviews": map[string]any{
			"nodes": []any{
				map[string]any{},
			},
		},
	}
	reviewers := extractReviewers(pr)

	if len(reviewers) != 0 {
		t.Errorf("expected 0 reviewers, got %d", len(reviewers))
	}
}

func TestExtractReviewers_DuplicateReviewers(t *testing.T) {
	pr := map[string]any{
		"reviews": map[string]any{
			"nodes": []any{
				map[string]any{
					"author": map[string]any{
						"login": "alice",
					},
				},
				map[string]any{
					"author": map[string]any{
						"login": "alice",
					},
				},
			},
		},
	}
	reviewers := extractReviewers(pr)

	if len(reviewers) != 1 {
		t.Errorf("expected 1 reviewer (deduplicated), got %d", len(reviewers))
	}
	if reviewers[0] != "alice" {
		t.Errorf("expected reviewer 'alice', got %q", reviewers[0])
	}
}

func TestFinder_parseBlameResults_NoRanges(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Missing ranges field
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{},
					},
				},
			},
		},
	}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for missing ranges field")
	}
}

func TestFinder_parseBlameResults_InvalidRange(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Invalid range (missing startingLine)
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"endingLine": float64(15),
									// Missing startingLine
									"commit": map[string]any{},
								},
							},
						},
					},
				},
			},
		},
	}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for invalid range")
	}
}

func TestFinder_parseBlameResults_InvalidPRNode(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Invalid prNode (not a map)
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												"invalid-node", // Not a map
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
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for invalid PR node")
	}
}

func TestFinder_parseBlameResults_NotMergedPR(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// PR that is not merged
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number": float64(123),
													"merged": false, // Not merged
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
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for non-merged PR")
	}
}

func TestFinder_parseBlameResults_DirectCommitWithNoAuthor(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Direct commit with no PR and no author
	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(15),
									"commit": map[string]any{
										"associatedPullRequests": map[string]any{
											"nodes": []any{}, // No PRs
										},
										// No author field
									},
								},
							},
						},
					},
				},
			},
		},
	}
	overlapping, all := finder.parseBlameResults(result, [][2]int{{10, 15}})

	if len(overlapping) != 0 || len(all) != 0 {
		t.Error("expected empty results for commit with no PR and no author")
	}
}

func TestFinder_recentCommitsInDirectory_CacheTypeAssertionFails(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Set invalid cached value (wrong type)
	cacheKey := "commits-dir:owner/repo:src:10"
	finder.cache.Set(cacheKey, "invalid-type") // String instead of []types.PRInfo

	// Mock a successful GraphQL response
	response := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"history": map[string]any{
							"nodes": []any{
								map[string]any{
									"oid": "abc123",
									"author": map[string]any{
										"user": map[string]any{
											"login": "alice",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	query := `
	query($owner: String!, $repo: String!, $path: String!, $limit: Int!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				name
				target {
					... on Commit {
						history(first: $limit, path: $path) {
							nodes {
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
	}`

	client.SetGraphQLResponse(query, response)

	// Should fall through cache type assertion and make GraphQL request
	prs, err := finder.recentCommitsInDirectory(ctx, "owner", "repo", "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get results from GraphQL query
	if len(prs) != 1 {
		t.Errorf("expected 1 PR from GraphQL query, got %d", len(prs))
	}
}

func TestFinder_recentPRsInProject_CacheTypeAssertionFails(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Set invalid cached value (wrong type)
	cacheKey := "prs-project:owner/repo"
	finder.cache.Set(cacheKey, 12345) // Int instead of []types.PRInfo

	// Mock a successful GraphQL response
	response := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
					},
					"nodes": []any{
						map[string]any{
							"number":   float64(100),
							"merged":   true,
							"mergedAt": "2024-01-01T12:00:00Z",
							"author": map[string]any{
								"login": "bob",
							},
							"mergedBy": map[string]any{
								"login": "alice",
							},
							"reviews": map[string]any{
								"nodes": []any{},
							},
						},
					},
				},
			},
		},
	}

	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				pageInfo {
					endCursor
					hasNextPage
				}
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
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
	}`

	client.SetGraphQLResponse(query, response)

	// Should fall through cache type assertion and make GraphQL request
	prs, err := finder.recentPRsInProject(ctx, "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get results from GraphQL query
	if len(prs) != 1 {
		t.Errorf("expected 1 PR from GraphQL query, got %d", len(prs))
	}

	if prs[0].Number != 100 {
		t.Errorf("expected PR #100, got #%d", prs[0].Number)
	}
}

func TestFinder_parseDirectoryCommitsFromGraphQL_NoNodes(t *testing.T) {
	finder := New(testutil.NewMockGitHubClient(), Config{PRCountCache: time.Hour})

	result := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"history": map[string]any{
							// Missing "nodes" field
						},
					},
				},
			},
		},
	}
	prs := finder.parseDirectoryCommitsFromGraphQL(result)

	if len(prs) != 0 {
		t.Errorf("expected 0 PRs, got %d", len(prs))
	}
}
