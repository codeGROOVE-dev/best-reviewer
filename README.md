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
- **For GitHub App mode**: 
  - GitHub App ID (found in your app settings)
  - GitHub App private key file (.pem file downloaded when creating the app)
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
# Using command-line flags
./better-reviewers --app-id "123456" --app-key "/path/to/private-key.pem"

# Using environment variables
export GITHUB_APP_ID="123456"
export GITHUB_APP_KEY="/path/to/private-key.pem"
./better-reviewers --app-id "" --app-key ""  # Flags can be empty to use env vars
```

### Polling Mode

```bash
./better-reviewers -project "owner/repo" -poll 1h
```

### Dry Run Mode

```bash
./better-reviewers -pr "owner/repo#123" -dry-run
```

## Configuration Options

### Command-Line Flags

- `-pr`: Pull request URL or reference
- `-project`: GitHub project to monitor
- `-org`: GitHub organization to monitor
- `--app-id`: GitHub App ID for authentication
- `--app-key`: Path to GitHub App private key file
- `-poll`: Polling interval (e.g., 1h, 30m)
- `-dry-run`: Run without making changes
- `-min-age`: Minimum time since last activity (default: 1h)
- `-max-age`: Maximum time since last activity (default: 180d)
- `-max-prs`: Maximum open PRs per reviewer (default: 9)
- `-pr-count-cache`: Cache duration for PR counts (default: 6h)

### Environment Variables

For GitHub App authentication:
- `GITHUB_APP_ID`: Your GitHub App's ID
- `GITHUB_APP_KEY`: Path to your app's private key file
- `GITHUB_APP_PRIVATE_KEY_PATH`: (Legacy) Alternative to GITHUB_APP_KEY

## GitHub App Setup

1. Create a GitHub App in your organization settings
2. Required permissions:
   - Repository: Read & Write (for PR assignments)
   - Pull requests: Read & Write
   - Organization members: Read
3. Download the private key when prompted
4. Note your App ID from the app settings page
5. Install the app on your organization(s)

## How It Works

1. **Analysis**: Examines PR changes, file history, and contributor patterns
2. **Scoring**: Rates candidates based on:
   - Code overlap with changed files
   - Recent activity and expertise
   - Current workload (open PRs)
3. **Selection**: Chooses optimal reviewers avoiding overloaded contributors
4. **Assignment**: Adds reviewers to PRs (unless in dry-run mode)

## Security Notes

- Private keys should have restricted permissions (not world-readable)
- JWT tokens are automatically refreshed before expiry
- All API responses are sanitized in logs
- Token validation ensures only valid GitHub tokens are accepted

## Testing

Run in dry-run mode to preview reviewer assignments:

```bash
./better-reviewers --dry-run --pr https://github.com/owner/repo/pull/123
```

## License

[License details here]