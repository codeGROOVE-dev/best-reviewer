# Better Reviewers

A Go program that intelligently finds and assigns reviewers for GitHub pull requests based on code context and reviewer activity.

## Features

- **Smart reviewer selection**: Context-based matching using code blame analysis and activity patterns
- **Workload balancing**: Filters out overloaded reviewers (>9 non-stale open PRs) 
- **Stale PR filtering**: Only counts PRs updated within 90 days for accurate workload assessment
- **Resilient API handling**: 25 retry attempts with exponential backoff (5s-20min) and intelligent caching
- **Bot detection**: Comprehensive filtering of bots, service accounts, and organizations
- **Multiple targets**: Single PR, project-wide, or organization-wide monitoring
- **Polling support**: Continuous monitoring with configurable intervals
- **Graceful degradation**: Continues operation even when secondary features fail
- **Comprehensive logging**: Detailed decision tracking and performance insights
- **Dry-run mode**: Test assignments without making changes

## Installation

```bash
go build -o better-reviewers
```

## Prerequisites

- Go 1.21 or later
- **For personal use**: GitHub CLI (`gh`) installed and authenticated
- **For GitHub App mode**: `GITHUB_APP_TOKEN` environment variable with valid app token
- GitHub token with appropriate permissions (repo access)

## Usage

### Single PR Analysis

```bash
./better-reviewers -pr "https://github.com/owner/repo/pull/123"
./better-reviewers -pr "owner/repo#123"
```

### Project Monitoring

```bash
./better-reviewers -project "owner/repo"
```

### Organization Monitoring

```bash
./better-reviewers -org "myorg"
```

### GitHub App Mode

Monitor all organizations where your GitHub App is installed:

```bash
export GITHUB_APP_TOKEN="your_app_token_here"
./better-reviewers -app
```

### Polling Mode

```bash
./better-reviewers -project "owner/repo" -poll 1h
```

### Dry Run Mode

```bash
./better-reviewers -pr "owner/repo#123" -dry-run
```

### Advanced Configuration

```bash
# Custom workload limits and caching
./better-reviewers -org "myorg" -max-prs 5 -pr-count-cache 12h

# Tight time constraints with extended polling
./better-reviewers -project "owner/repo" -poll 30m -min-age 30m -max-age 7d

# GitHub App monitoring with polling
export GITHUB_APP_TOKEN="your_app_token_here"
./better-reviewers -app -poll 2h -dry-run
```

## Command Line Options

### Target Flags (Mutually Exclusive)

- `-pr`: Pull request URL or shorthand (e.g., `https://github.com/owner/repo/pull/123` or `owner/repo#123`)
- `-project`: GitHub project to monitor (e.g., `owner/repo`)
- `-org`: GitHub organization to monitor
- `-app`: Monitor all organizations where this GitHub app is installed

### Behavior Flags

- `-poll`: Polling interval (e.g., `1h`, `30m`). If not set, runs once
- `-dry-run`: Run in dry-run mode (no actual reviewer assignments)
- `-min-age`: Minimum time since last commit or review for PR assignment (default: 1h)
- `-max-age`: Maximum time since last commit or review for PR assignment (default: 180 days)
- `-max-prs`: Maximum non-stale open PRs a candidate can have before being filtered out (default: 9)
- `-pr-count-cache`: Cache duration for PR count queries to optimize performance (default: 6h)

## How It Works

### Reviewer Selection Algorithm

The program finds exactly two reviewers for each PR: a **primary reviewer** (with author context) and a **secondary reviewer** (who actively reviews code).

#### Primary Reviewer (Author Context)

The primary reviewer is selected based on who knows the code best, in this priority order:

1. **Blame-based Authors**:
   - Examines GitHub blame history for changed files
   - Considers the top 5 PRs by overlap with edited lines
   - Selects authors of these PRs who still have write access
   - Verifies write access via author_association

2. **Directory Author** (fallback):
   - Most recent author of a merged PR in the same directory

3. **Project Author** (fallback):
   - Most recent author of a merged PR in the project

#### Secondary Reviewer (Active Reviewer)

The secondary reviewer is selected based on review activity, in this priority order:

1. **Blame-based Reviewers**:
   - Examines the same GitHub blame history
   - Considers the top 5 PRs by overlap with edited lines
   - Selects reviewers/approvers of these PRs

2. **Directory Reviewer** (fallback):
   - Most recent reviewer of a merged PR in the same directory

3. **Project Reviewer** (fallback):
   - Most recent reviewer of a merged PR in the project

### Assignment Rules

- Always attempts to assign exactly 2 reviewers: primary and secondary
- The PR author cannot be a reviewer
- Primary and secondary must be different people
- Each reviewer selection logs the mechanism used (e.g., "primary-blame-author", "secondary-directory-reviewer")
- **Draft PRs**: Skips reviewer assignment for draft PRs but logs who would have been assigned

### Error Handling

- If after exhausting all fallback mechanisms, the only candidate found is the PR author, the program will error with a clear message
- This ensures that the PR author is never assigned as their own reviewer
- The error message will indicate that all candidates have been exhausted

### GitHub API Usage

The program uses:
- **GitHub REST API v3** for PR data, file changes, and reviews
- **GitHub GraphQL API v4** for blame data and efficient directory/project searches  
- **GitHub Search API** for PR count queries with workload balancing
- **Intelligent caching**: 6-hour cache for PR counts, 20-day cache for PR data, failure caching
- **Robust retry logic**: 25 attempts with exponential backoff (5s initial, 20min max delay)
- **Timeout management**: 30-second timeouts for search queries, 120-second for other calls
- **Graceful degradation**: Continues operation when non-critical APIs fail
- **Flexible authentication**: `gh auth token` for personal use or `GITHUB_APP_TOKEN` for app installations

## Architecture

The codebase is organized into several key files:

- `main.go`: Command-line interface and main program logic
- `github.go`: GitHub API client implementation
- `reviewer.go`: Core reviewer finding and assignment logic
- `analysis.go`: Blame data analysis and PR relationship detection
- `main_test.go`: Unit tests for core functionality

## Testing

Run the test suite:

```bash
go test -v
```

## Example Output

### Workload Balancing in Action
```
2024/01/15 10:30:45 Processing PR owner/repo#123: Add new feature
2024/01/15 10:30:45 [CACHE] User type cache hit for alice: User
2024/01/15 10:30:45     📊 User alice has 3 non-stale open PRs in org myorg (2 assigned, 1 for review)
2024/01/15 10:30:45 [CACHE] User type cache hit for bob: User  
2024/01/15 10:30:45     📊 User bob has 12 non-stale open PRs in org myorg (8 assigned, 4 for review)
2024/01/15 10:30:45     Filtered (too many open PRs 12 > 9 in org myorg): bob
2024/01/15 10:30:45 [CACHE] User type cache hit for charlie: User
2024/01/15 10:30:45     📊 User charlie has 5 non-stale open PRs in org myorg (3 assigned, 2 for review)
2024/01/15 10:30:45 Found 2 reviewer candidates for PR 123
2024/01/15 10:30:45 Adding reviewers [alice charlie] to PR owner/repo#123
2024/01/15 10:30:46 Successfully added reviewers [alice charlie] to PR 123
```

### Fallback Mechanism in Action
```
2024/01/15 10:31:00 Processing PR owner/repo#124: Fix minor bug
2024/01/15 10:31:00 Analyzing 3 changed files for PR 124
2024/01/15 10:31:01 === Finding PRIMARY reviewer (author context) ===
2024/01/15 10:31:01 Checking blame-based authors for primary reviewer
2024/01/15 10:31:01 No blame-based authors found, checking directory authors
2024/01/15 10:31:02 PRIMARY reviewer selected: charlie (method: primary-directory-author)
2024/01/15 10:31:02 === Finding SECONDARY reviewer (active reviewer) ===
2024/01/15 10:31:02 Checking blame-based reviewers for secondary reviewer
2024/01/15 10:31:02 No blame-based reviewers found, checking directory reviewers
2024/01/15 10:31:03 No directory reviewers found, checking project reviewers
2024/01/15 10:31:03 SECONDARY reviewer selected: dave (method: secondary-project-reviewer)
2024/01/15 10:31:03 Found 2 reviewer candidates for PR 124
2024/01/15 10:31:03 Adding reviewers [charlie dave] to PR owner/repo#124
2024/01/15 10:31:03 Successfully added reviewers [charlie dave] to PR 124
```

### Error Case: Only PR Author Found
```
2024/01/15 10:32:00 Processing PR owner/repo#125: Initial commit
2024/01/15 10:32:00 Top changed files for PR 125: [main.go, go.mod, README.md]
2024/01/15 10:32:01 Finding context reviewers using blame data for 3 files
2024/01/15 10:32:01 Finding activity reviewers for 3 files
2024/01/15 10:32:01 Found only 0 candidates, trying fallback to line authors
2024/01/15 10:32:01 Finding line authors with write access for fallback
2024/01/15 10:32:01 Line author fallback candidate: john-doe (lines: 50, association: OWNER, method: fallback-line-author)
2024/01/15 10:32:02 Found only 1 candidates, trying fallback to recent file authors
2024/01/15 10:32:02 Found only 1 candidates, trying directory-based fallbacks
2024/01/15 10:32:02 Found only 1 candidates, trying project-wide fallbacks
2024/01/15 10:32:03 Project author fallback candidate: john-doe (method: fallback-project-author)
2024/01/15 10:32:03 Failed to find reviewer candidates: exhausted all reviewer candidates: the only candidate found was the PR author (john-doe)
```

### Draft PR Handling
```
2024/01/15 10:33:00 Processing PR owner/repo#126 [DRAFT]: WIP: New feature
2024/01/15 10:33:00 Top changed files for PR 126: [feature.go, feature_test.go]
2024/01/15 10:33:01 Finding context reviewers using blame data for 2 files
2024/01/15 10:33:01 Context reviewer candidate: alice (score: 20, method: context-blame-approver)
2024/01/15 10:33:01 Activity reviewer candidate: bob (PR size: 120, method: activity-recent-approver)
2024/01/15 10:33:01 Found 2 reviewer candidates for PR 126
2024/01/15 10:33:01 Selected reviewer: alice (method: context-blame-approver, context score: 20, activity score: 0)
2024/01/15 10:33:01 Selected reviewer: bob (method: activity-recent-approver, context score: 0, activity score: 120)
2024/01/15 10:33:01 PR 126 is a draft - skipping reviewer assignment
2024/01/15 10:33:01 Would have assigned reviewers [alice bob] to PR 126 if it wasn't a draft
```

## Contributing

1. Follow Go best practices and code review guidelines from go.dev
2. Add tests for new functionality
3. Ensure comprehensive logging for debugging
4. Use minimal external dependencies

## License

This project is provided as-is for educational and productivity purposes.