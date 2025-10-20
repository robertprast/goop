# Google Organization Gemini AI Workflow Examples

This document contains examples of GitHub Actions workflows from Google's organization repositories that use Gemini AI to automatically review and triage issues and issue comments.

## Overview

Google has implemented Gemini AI-powered workflows across several of their repositories to automate issue management tasks such as:
- Automatic triage when issues are opened or reopened
- Scheduled triage of unlabeled or untriaged issues
- On-demand triage triggered by comments
- Automated labeling based on issue content analysis

## Example 1: Automated Issue Triage (google/draco)

**Source**: [google/draco/.github/workflows/gemini-issue-automated-triage.yml](https://github.com/google/draco/blob/3abbc66fdf5597b1560c44ce7840aac76900b3f7/.github/workflows/gemini-issue-automated-triage.yml)

This workflow automatically triages issues when they are opened, reopened, or when a user with appropriate permissions comments with `@gemini-cli /triage`.

```yaml
name: '🏷️ Gemini Automated Issue Triage'

on:
  issues:
    types:
      - 'opened'
      - 'reopened'
  issue_comment:
    types:
      - 'created'
  workflow_dispatch:
    inputs:
      issue_number:
        description: 'issue number to triage'
        required: true
        type: 'number'

concurrency:
  group: '${{ github.workflow }}-${{ github.event.issue.number }}'
  cancel-in-progress: true

defaults:
  run:
    shell: 'bash'

permissions:
  contents: 'read'
  id-token: 'write'
  issues: 'write'
  statuses: 'write'

jobs:
  triage-issue:
    if: |-
      github.event_name == 'issues' ||
      github.event_name == 'workflow_dispatch' ||
      (
        github.event_name == 'issue_comment' &&
        contains(github.event.comment.body, '@gemini-cli /triage') &&
        contains(fromJSON('["OWNER", "MEMBER", "COLLABORATOR"]'), github.event.comment.author_association)
      )
    timeout-minutes: 5
    runs-on: 'ubuntu-latest'

    steps:
      - name: 'Checkout repository'
        uses: 'actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683' # ratchet:actions/checkout@v4

      - name: 'Generate GitHub App Token'
        id: 'generate_token'
        if: |-
          ${{ vars.APP_ID }}
        uses: 'actions/create-github-app-token@df432ceedc7162793a195dd1713ff69aefc7379e' # ratchet:actions/create-github-app-token@v2
        with:
          app-id: '${{ vars.APP_ID }}'
          private-key: '${{ secrets.APP_PRIVATE_KEY }}'

      - name: 'Run Gemini Issue Triage'
        uses: 'google-github-actions/run-gemini-cli@v0'
        id: 'gemini_issue_triage'
        env:
          GITHUB_TOKEN: '${{ steps.generate_token.outputs.token || secrets.GITHUB_TOKEN }}'
          ISSUE_TITLE: '${{ github.event.issue.title }}'
          ISSUE_BODY: '${{ github.event.issue.body }}'
          ISSUE_NUMBER: '${{ github.event.issue.number }}'
          REPOSITORY: '${{ github.repository }}'
        with:
          gemini_cli_version: '${{ vars.GEMINI_CLI_VERSION }}'
          gcp_workload_identity_provider: '${{ vars.GCP_WIF_PROVIDER }}'
          gcp_project_id: '${{ vars.GOOGLE_CLOUD_PROJECT }}'
          gcp_location: '${{ vars.GOOGLE_CLOUD_LOCATION }}'
          gcp_service_account: '${{ vars.SERVICE_ACCOUNT_EMAIL }}'
          gemini_api_key: '${{ secrets.GEMINI_API_KEY }}'
          use_vertex_ai: '${{ vars.GOOGLE_GENAI_USE_VERTEXAI }}'
          use_gemini_code_assist: '${{ vars.GOOGLE_GENAI_USE_GCA }}'
          settings: |-
            {
              "maxSessionTurns": 25,
              "coreTools": [
                "run_shell_command(echo)",
                "run_shell_command(gh label list)",
                "run_shell_command(gh issue edit)"
              ],
              "telemetry": {
                "enabled": false,
                "target": "gcp"
              }
            }
          prompt: |-
            ## Role

            You are an issue triage assistant. Analyze the current GitHub issue
            and apply the most appropriate existing labels. Use the available
            tools to gather information; do not ask for information to be
            provided.

            ## Steps

            1. Run: `gh label list` to get all available labels.
            2. Review the issue title and body provided in the environment
               variables: "${ISSUE_TITLE}" and "${ISSUE_BODY}".
            3. Classify issues by their kind (bug, enhancement, documentation,
               cleanup, etc) and their priority (p0, p1, p2, p3). Set the
               labels accoridng to the format `kind/*` and `priority/*` patterns.
            4. Apply the selected labels to this issue using:
               `gh issue edit "${ISSUE_NUMBER}" --add-label "label1,label2"`
            5. If the "status/needs-triage" label is present, remove it using:
               `gh issue edit "${ISSUE_NUMBER}" --remove-label "status/needs-triage"`

            ## Guidelines

            - Only use labels that already exist in the repository
            - Do not add comments or modify the issue content
            - Triage only the current issue
            - Assign all applicable labels based on the issue content
            - Reference all shell variables as "${VAR}" (with quotes and braces)

      - name: 'Post Issue Triage Failure Comment'
        if: |-
          ${{ failure() && steps.gemini_issue_triage.outcome == 'failure' }}
        uses: 'actions/github-script@60a0d83039c74a4aee543508d2ffcb1c3799cdea'
        with:
          github-token: '${{ steps.generate_token.outputs.token || secrets.GITHUB_TOKEN }}'
          script: |-
            github.rest.issues.createComment({
              owner: '${{ github.repository }}'.split('/')[0],
              repo: '${{ github.repository }}'.split('/')[1],
              issue_number: '${{ github.event.issue.number }}',
              body: 'There is a problem with the Gemini CLI issue triaging. Please check the [action logs](${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}) for details.'
            })
```

### Key Features:
- **Triggers**: Runs on issue opened/reopened, or when collaborators comment with `@gemini-cli /triage`
- **Concurrency Control**: Only one triage per issue at a time
- **GitHub App Token**: Optional GitHub App authentication for enhanced permissions
- **Gemini CLI Action**: Uses `google-github-actions/run-gemini-cli@v0` to invoke Gemini AI
- **Automated Labeling**: Analyzes issue and applies appropriate `kind/*` and `priority/*` labels
- **Error Handling**: Posts a comment if triage fails

## Example 2: Scheduled Issue Triage (google/draco)

**Source**: [google/draco/.github/workflows/gemini-issue-scheduled-triage.yml](https://github.com/google/draco/blob/3abbc66fdf5597b1560c44ce7840aac76900b3f7/.github/workflows/gemini-issue-scheduled-triage.yml)

This workflow runs on a schedule to find and triage issues that don't have labels or need triage.

```yaml
name: '📋 Gemini Scheduled Issue Triage'

on:
  schedule:
    - cron: '0 * * * *' # Runs every hour
  workflow_dispatch:

concurrency:
  group: '${{ github.workflow }}'
  cancel-in-progress: true

defaults:
  run:
    shell: 'bash'

permissions:
  contents: 'read'
  id-token: 'write'
  issues: 'write'
  statuses: 'write'

jobs:
  triage-issues:
    timeout-minutes: 5
    runs-on: 'ubuntu-latest'

    steps:
      - name: 'Checkout repository'
        uses: 'actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683' # ratchet:actions/checkout@v4

      - name: 'Generate GitHub App Token'
        id: 'generate_token'
        if: |-
          ${{ vars.APP_ID }}
        uses: 'actions/create-github-app-token@df432ceedc7162793a195dd1713ff69aefc7379e' # ratchet:actions/create-github-app-token@v2
        with:
          app-id: '${{ vars.APP_ID }}'
          private-key: '${{ secrets.APP_PRIVATE_KEY }}'

      - name: 'Find untriaged issues'
        id: 'find_issues'
        env:
          GITHUB_TOKEN: '${{ steps.generate_token.outputs.token || secrets.GITHUB_TOKEN }}'
          GITHUB_REPOSITORY: '${{ github.repository }}'
          GITHUB_OUTPUT: '${{ github.output }}'
        run: |-
          set -euo pipefail

          echo '🔍 Finding issues without labels...'
          NO_LABEL_ISSUES="$(gh issue list --repo "${GITHUB_REPOSITORY}" \
            --search 'is:open is:issue no:label' --json number,title,body)"

          echo '🏷️ Finding issues that need triage...'
          NEED_TRIAGE_ISSUES="$(gh issue list --repo "${GITHUB_REPOSITORY}" \
            --search 'is:open is:issue label:"status/needs-triage"' --json number,title,body)"

          echo '🔄 Merging and deduplicating issues...'
          ISSUES="$(echo "${NO_LABEL_ISSUES}" "${NEED_TRIAGE_ISSUES}" | jq -c -s 'add | unique_by(.number)')"

          echo '📝 Setting output for GitHub Actions...'
          echo "issues_to_triage=${ISSUES}" >> "${GITHUB_OUTPUT}"

          ISSUE_COUNT="$(echo "${ISSUES}" | jq 'length')"
          echo "✅ Found ${ISSUE_COUNT} issues to triage! 🎯"

      - name: 'Run Gemini Issue Triage'
        if: |-
          ${{ steps.find_issues.outputs.issues_to_triage != '[]' }}
        uses: 'google-github-actions/run-gemini-cli@v0'
        id: 'gemini_issue_triage'
        env:
          GITHUB_TOKEN: '${{ steps.generate_token.outputs.token || secrets.GITHUB_TOKEN }}'
          ISSUES_TO_TRIAGE: '${{ steps.find_issues.outputs.issues_to_triage }}'
          REPOSITORY: '${{ github.repository }}'
        with:
          gemini_cli_version: '${{ vars.GEMINI_CLI_VERSION }}'
          gcp_workload_identity_provider: '${{ vars.GCP_WIF_PROVIDER }}'
          gcp_project_id: '${{ vars.GOOGLE_CLOUD_PROJECT }}'
          gcp_location: '${{ vars.GOOGLE_CLOUD_LOCATION }}'
          gcp_service_account: '${{ vars.SERVICE_ACCOUNT_EMAIL }}'
          gemini_api_key: '${{ secrets.GEMINI_API_KEY }}'
          use_vertex_ai: '${{ vars.GOOGLE_GENAI_USE_VERTEXAI }}'
          use_gemini_code_assist: '${{ vars.GOOGLE_GENAI_USE_GCA }}'
          settings: |-
            {
              "maxSessionTurns": 25,
              "coreTools": [
                "run_shell_command(echo)",
                "run_shell_command(gh label list)",
                "run_shell_command(gh issue edit)",
                "run_shell_command(gh issue list)"
              ],
              "telemetry": {
                "enabled": false,
                "target": "gcp"
              }
            }
          prompt: |-
            ## Role

            You are an issue triage assistant. Analyze issues and apply
            appropriate labels. Use the available tools to gather information;
            do not ask for information to be provided.

            ## Steps

            1. Run: `gh label list`
            2. Check environment variable: "${ISSUES_TO_TRIAGE}" (JSON array
               of issues)
            3. For each issue, apply labels:
               `gh issue edit "${ISSUE_NUMBER}" --add-label "label1,label2"`.
               If available, set labels that follow the `kind/*`, `area/*`,
               and `priority/*` patterns.
            4. For each issue, if the `status/needs-triage` label is present,
               remove it using:
               `gh issue edit "${ISSUE_NUMBER}" --remove-label "status/needs-triage"`

            ## Guidelines

            - Only use existing repository labels
            - Do not add comments
            - Triage each issue independently
            - Reference all shell variables as "${VAR}" (with quotes and braces)
```

### Key Features:
- **Scheduled Runs**: Executes hourly to catch any untriaged issues
- **Batch Processing**: Finds all issues without labels or with `status/needs-triage` label
- **Deduplication**: Merges and deduplicates issues before processing
- **Batch Triage**: Processes multiple issues in a single Gemini CLI session
- **Conditional Execution**: Only runs Gemini if there are issues to triage

## Additional Examples

### Other Google Repositories Using Gemini Workflows:

1. **google/adk-samples**: Similar automated and scheduled triage workflows
   - [gemini-issue-automated-triage.yml](https://github.com/google/adk-samples/blob/main/.github/workflows/gemini-issue-automated-triage.yml)
   - [gemini-issue-scheduled-triage.yml](https://github.com/google/adk-samples/blob/main/.github/workflows/gemini-issue-scheduled-triage.yml)

2. **google/syzkaller**: PR review workflow
   - [gemini-pr-review.yml](https://github.com/google/syzkaller/blob/master/.github/workflows/gemini-pr-review.yml)

3. **google/secops-wrapper**: Gemini review and dispatch workflows
   - [gemini-review.yml](https://github.com/google/secops-wrapper/blob/main/.github/workflows/gemini-review.yml)
   - [gemini-dispatch.yml](https://github.com/google/secops-wrapper/blob/main/.github/workflows/gemini-dispatch.yml)

## Configuration Requirements

To implement these workflows in your repository, you'll need:

### Required Secrets:
- `GEMINI_API_KEY`: Gemini API key (if not using Vertex AI)
- `APP_PRIVATE_KEY`: GitHub App private key (optional, for enhanced permissions)

### Required Variables:
- `GEMINI_CLI_VERSION`: Version of Gemini CLI to use
- `GOOGLE_CLOUD_PROJECT`: GCP project ID (for Vertex AI)
- `GOOGLE_CLOUD_LOCATION`: GCP location (for Vertex AI)
- `SERVICE_ACCOUNT_EMAIL`: Service account email (for Vertex AI)
- `GCP_WIF_PROVIDER`: Workload Identity Federation provider (for Vertex AI)
- `GOOGLE_GENAI_USE_VERTEXAI`: Whether to use Vertex AI (boolean)
- `GOOGLE_GENAI_USE_GCA`: Whether to use Gemini Code Assist (boolean)
- `APP_ID`: GitHub App ID (optional)

### Required Permissions:
```yaml
permissions:
  contents: 'read'
  id-token: 'write'      # For GCP Workload Identity Federation
  issues: 'write'        # To label and edit issues
  statuses: 'write'      # To update status
```

## Benefits

These Gemini AI-powered workflows provide:
- **Automated Triage**: Reduces manual effort in categorizing issues
- **Consistent Labeling**: Applies labels consistently based on issue content
- **Faster Response**: Issues are triaged immediately upon creation
- **Catch-all Scheduling**: Hourly scheduled runs catch any issues that were missed
- **Team Collaboration**: Team members can trigger re-triage with comments
- **Scalability**: Handles multiple issues efficiently in batch mode

## References

- [Google GitHub Actions - Gemini CLI](https://github.com/google-github-actions/run-gemini-cli)
- [Gemini API Documentation](https://ai.google.dev/docs)
- [GitHub Actions Documentation](https://docs.github.com/en/actions)
