#!/usr/bin/env python3
"""
Upload JUnit test results and commit metadata to BigQuery.

Can be run locally for testing by setting environment variables
and pointing to downloaded test artifacts.
"""

import json
import os
import sys
import xml.etree.ElementTree as ET
from datetime import datetime
from pathlib import Path
import subprocess

def run_git(cmd):
    """Run a git command and return output, or empty string on failure."""
    try:
        result = subprocess.run(
            ['git'] + cmd.split(),
            capture_output=True,
            text=True,
            timeout=10
        )
        return result.stdout.strip()
    except Exception:
        return ''


def get_bigquery_client():
    """Create BigQuery client from service account key in environment."""
    from google.cloud import bigquery

    sa_key_json = os.environ.get('GCP_SERVICE_ACCOUNT_KEY')
    if not sa_key_json:
        print("Error: GCP_SERVICE_ACCOUNT_KEY environment variable not set", file=sys.stderr)
        sys.exit(1)

    sa_key = json.loads(sa_key_json)
    return bigquery.Client.from_service_account_info(sa_key)

def parse_junit_xml(xml_path):
    """Parse a single JUnit XML file and yield test results."""
    try:
        tree = ET.parse(xml_path)
        root = tree.getroot()

        for testcase in root.findall('.//testcase'):
            classname = testcase.get('classname', '')
            name = testcase.get('name', '')
            time_sec = float(testcase.get('time', 0))
            duration_ms = int(time_sec * 1000)

            failure = testcase.find('failure')
            error = testcase.find('error')
            skipped = testcase.find('skipped')

            if failure is not None:
                status = 'failed'
                stack_trace = (failure.get('message', '') + '\n' + (failure.text or ''))[:10000]
            elif error is not None:
                status = 'error'
                stack_trace = (error.get('message', '') + '\n' + (error.text or ''))[:10000]
            elif skipped is not None:
                status = 'skipped'
                stack_trace = None
            else:
                status = 'passed'
                stack_trace = None

            yield {
                'test_name': f"{classname}.{name}",
                'status': status,
                'duration_ms': duration_ms,
                'stack_trace': stack_trace
            }
    except Exception as e:
        print(f"Error parsing {xml_path}: {e}", file=sys.stderr)


def collect_test_results(test_results_dir):
    """Collect all test results from JUnit XML files in directory."""
    results = []
    results_path = Path(test_results_dir)

    if not results_path.exists():
        print(f"Warning: Test results directory not found: {results_path}", file=sys.stderr)
        return results

    for xml_file in results_path.glob("**/*.xml"):
        for result in parse_junit_xml(xml_file):
            results.append(result)

    return results


def build_commit_metadata(github_run_id, commit_hash, branch, pr_number, pr_base_branch, pr_base_commit):
    """Build commit metadata dict from git and environment."""
    is_anchor_branch = branch == 'main' or branch.startswith('upgrade-v')

    parent_commits = run_git('log -1 --pretty=%P')
    commit_message = run_git('log -1 --pretty=%s')[:500]
    commit_author = run_git('log -1 --pretty=%an <%ae>')
    commit_timestamp_str = run_git('log -1 --pretty=%cI')

    commit_ts = None
    if commit_timestamp_str:
        try:
            commit_ts = datetime.fromisoformat(commit_timestamp_str).isoformat()
        except Exception:
            pass

    return {
        'run_id': github_run_id,
        'commit_hash': commit_hash,
        'branch': branch,
        'is_anchor_branch': is_anchor_branch,
        'pr_number': pr_number or None,
        'pr_base_branch': pr_base_branch or None,
        'pr_base_commit': pr_base_commit or None,
        'parent_commits': parent_commits or None,
        'commit_message': commit_message or None,
        'commit_author': commit_author or None,
        'commit_timestamp': commit_ts
    }


def upload_to_bigquery(client, project, dataset, test_results, commit_metadata, dry_run=False):
    """Upload test results and commit metadata to BigQuery."""

    if dry_run:
        print("\n=== DRY RUN - Would upload: ===")
        print(f"\nCommit metadata:")
        print(json.dumps(commit_metadata, indent=2, default=str))
        print(f"\nTest results ({len(test_results)} total):")
        for r in test_results[:5]:
            print(f"  {r['status']:8} {r['test_name'][:60]}")
        if len(test_results) > 5:
            print(f"  ... and {len(test_results) - 5} more")
        return True

    # Upload test results
    if test_results:
        table_id = f"{project}.{dataset}.test_runs"
        errors = client.insert_rows_json(table_id, test_results)
        if errors:
            print(f"BigQuery insert errors (test_runs): {errors}", file=sys.stderr)
            return False
        print(f"Inserted {len(test_results)} test results")

    # Check if commit metadata already exists (dedup for matrix jobs)
    check_query = f"""
        SELECT COUNT(*) as cnt 
        FROM `{project}.{dataset}.commit_metadata` 
        WHERE run_id = '{commit_metadata['run_id']}'
    """
    try:
        result = list(client.query(check_query).result())
        already_exists = result[0].cnt > 0
    except Exception:
        already_exists = False

    if not already_exists:
        table_id = f"{project}.{dataset}.commit_metadata"
        errors = client.insert_rows_json(table_id, [commit_metadata])
        if errors:
            print(f"BigQuery insert errors (commit_metadata): {errors}", file=sys.stderr)
            return False
        print(f"Inserted commit metadata for {commit_metadata['commit_hash'][:8]}")
    else:
        print(f"Commit metadata already exists for run {commit_metadata['run_id']}")

    return True


def main():
    import argparse

    parser = argparse.ArgumentParser(description='Upload test results to BigQuery')
    parser.add_argument('--test-results-dir',
                        default='./testermint/build/test-results',
                        help='Directory containing JUnit XML files')
    parser.add_argument('--dataset', default='integration_results',
                        help='BigQuery dataset name')
    parser.add_argument('--dry-run', action='store_true',
                        help='Print what would be uploaded without actually uploading')
    args = parser.parse_args()

    # Get environment variables (with sensible defaults for local testing)
    # Replace the argparse project argument with:
    project = os.environ.get('GCP_PROJECT_ID')
    if not project and not args.dry_run:
        print("Error: GCP_PROJECT_ID environment variable not set", file=sys.stderr)
        sys.exit(1)
    github_run_id = os.environ.get('GITHUB_RUN_ID', f'local-{datetime.now().strftime("%Y%m%d-%H%M%S")}')
    test_group = os.environ.get('MATRIX_TEST_GROUP', 'all')
    run_id = f"{github_run_id}-{test_group}"
    commit_hash = os.environ.get('GITHUB_SHA', run_git('rev-parse HEAD') or 'unknown')
    run_timestamp = datetime.utcnow().isoformat()

    # Branch detection
    ref = os.environ.get('GITHUB_REF', '')
    head_ref = os.environ.get('GITHUB_HEAD_REF', '')
    base_ref = os.environ.get('GITHUB_BASE_REF', '')

    if head_ref:
        branch = head_ref
    elif ref.startswith('refs/heads/'):
        branch = ref.replace('refs/heads/', '')
    elif ref:
        branch = ref
    else:
        branch = run_git('rev-parse --abbrev-ref HEAD') or 'unknown'

    pr_number = os.environ.get('PR_NUMBER', '')
    pr_base_branch = base_ref
    pr_base_commit = os.environ.get('PR_BASE_COMMIT', '')

    # URLs
    repo = os.environ.get('GITHUB_REPOSITORY', 'unknown/unknown')
    test_report_url = os.environ.get('TEST_REPORT_URL', '')
    logs_url = os.environ.get('LOGS_URL', '')

    # Collect test results
    print(f"Parsing test results from: {args.test_results_dir}")
    raw_results = collect_test_results(args.test_results_dir)

    if not raw_results:
        print("No test results found!")
        if not args.dry_run:
            sys.exit(1)

    # Enrich with run metadata
    test_results = []
    for r in raw_results:
        test_results.append({
            'run_id': run_id,
            'branch': branch,
            'commit_hash': commit_hash,
            'run_timestamp': run_timestamp,
            'test_group': test_group,
            'test_name': r['test_name'],
            'status': r['status'],
            'duration_ms': r['duration_ms'],
            'stack_trace': r['stack_trace'],
            'test_report_url': test_report_url,
            'logs_url': logs_url
        })

    # Build commit metadata
    commit_metadata = build_commit_metadata(
        github_run_id, commit_hash, branch, pr_number, pr_base_branch, pr_base_commit
    )

    # Summary
    failed_count = sum(1 for t in test_results if t['status'] in ('failed', 'error'))
    passed_count = sum(1 for t in test_results if t['status'] == 'passed')
    skipped_count = sum(1 for t in test_results if t['status'] == 'skipped')
    print(f"\nFound: {passed_count} passed, {failed_count} failed, {skipped_count} skipped")
    print(f"Branch: {branch}, Commit: {commit_hash[:8]}")

    # Upload
    client = None if args.dry_run else get_bigquery_client()
    success = upload_to_bigquery(
        client, project, args.dataset,
        test_results, commit_metadata,
        dry_run=args.dry_run
    )

    sys.exit(0 if success else 1)


if __name__ == '__main__':
    main()